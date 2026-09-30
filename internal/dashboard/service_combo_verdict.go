// service_combo_verdict.go —— D-563 S3：组合池判定的腿级保证金需求（杠杆感知）。
// 判据单源（P3）：白名单详情徽标（ListXAssetAllowCombos）与待入场等待原因
// （probeGates 闸6 后）共调 comboPoolVerdict——两表面同结论消「假锁死」矛盾。
// 三态：ok=池够 / conflict=池缺或保证金需求>可用（红=结构性锁死） /
// unknown=杠杆数据缺（黄=待定，不谎报锁死）。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
)

// legMarginNeedFrac 腿保证金需求率（判定口径=实数不猜估）：
// MT=init_margin_rate fact（D-099 事件采集；≤24h 新鲜；D-006 S1b：缺/陈→悲观档 1/20 估+注记）；
// perp=venue 杠杆实读 1/lev（D-572 S1：读穿缓存 5min——面板渲染不打爆 REST；
// 读败=unknown 黄标不谎报）；unified=杠杆读不到→unknown；
// 非保证金腿（spot 等）=名义全款 1.0。
func (s *Service) legMarginNeedFrac(ctx context.Context, venueKey, sym string, sellLeg bool) (frac float64, note string, known bool) {
	k := keydomain.Parse(venueKey)
	switch k.Type {
	case "mt4", "mt5":
		fs, err := s.gradDB().LatestFacts(ctx, fact.KindInitMarginRate, k.Venue, "")
		if rate, measured := effMTMarginRate(fs, err, time.Now()); measured {
			return rate, "", true
		} else {
			return rate, "init_margin_rate 未采或超 24h（按悲观档 20x 估）", true
		}
	case "perp":
		return s.legMarginNeedPerp(ctx, venueKey, sym, sellLeg)
	case "unified":
		return 0, "unified 杠杆读不到", false
	default:
		return 1.0, "", true
	}
}

// legMarginNeedPerp D-572 S1：perp 腿 venue 杠杆实读透出（D-565 venueLeverage
// 通道复用不重建；执行链 precheck 实读权威语义不变——面板=展示面）。
// 读穿缓存 5min（键=vk|sym|side，对齐 snapshotStaleWindow 哲学；命中 note 时刻
// =缓存 at 展示真读时刻）；读败→unknown 黄标诚实不猜。
func (s *Service) legMarginNeedPerp(ctx context.Context, vk, sym string, sellLeg bool) (float64, string, bool) {
	side := "buy"
	if sellLeg {
		side = "sell"
	}
	key := vk + "|" + sym + "|" + side
	s.levCacheMu.Lock()
	if s.levCache == nil {
		s.levCache = map[string]levCacheEntry{}
	}
	if e, ok := s.levCache[key]; ok && time.Since(e.at) <= 5*time.Minute {
		s.levCacheMu.Unlock()
		return 1 / e.lev, fmt.Sprintf("%s 杠杆 %.0f×（实读 %s）", vk, e.lev, e.at.Format("15:04:05")), true
	}
	s.levCacheMu.Unlock()
	lev, err := s.venueLeverage(ctx, vk, sym, sellLeg)
	if err != nil || lev <= 0 {
		return 0, "杠杆实况不可得（venue 读败）", false
	}
	at := time.Now()
	s.levCacheMu.Lock()
	s.levCache[key] = levCacheEntry{lev: lev, at: at}
	s.levCacheMu.Unlock()
	return 1 / lev, fmt.Sprintf("%s 杠杆 %.0f×（实读 %s）", vk, lev, at.Format("15:04:05")), true
}

// levCacheEntry D-572 S1：杠杆读穿缓存条目（frac 由 1/lev 当场算不冗余存）。
type levCacheEntry struct {
	lev float64
	at  time.Time
}

// comboPoolVerdict 组合腿池判定：need=腿名义×保证金率 vs 池可用。
// 腿名义=NotionalUSD（缺省回 MaxGrossUSD/2，与旧判据同基数）。
func (s *Service) comboPoolVerdict(ctx context.Context, c pgstore.XAssetComboAdminRow, snaps []pgstore.VenueAccountLatest) (verdict, detail string) {
	need := c.MaxGrossUSD / 2
	if c.NotionalUSD != nil {
		need = *c.NotionalUSD
	}
	var unknown []string
	for i, vk := range [2]string{c.VenueBuy, c.VenueSell} {
		p, ok := s.legPool(ctx, snaps, vk)
		if !ok {
			return "conflict", fmt.Sprintf("腿池数据缺：%s 无可用读数（结构性锁死）", vk)
		}
		// D-565 S3：快照池龄闸（与 sizing 同式）——非 live 且 >5min→按池缺处理
		// （unknown 灰标不谎报锁死；两表面同源自动一致）。
		if !p.live && time.Since(p.at) > snapshotStaleWindow {
			unknown = append(unknown, fmt.Sprintf("%s（池快照过期 %s>5min）", vk, p.at.Format("15:04:05")))
			continue
		}
		avail := p.equity
		if p.free != nil {
			avail = *p.free
		}
		// D-572 S1：venue 原生符号解析（与 alloc_risk :160-162 同源同法——
		// qbus.Latest(canon,vk).VenueSymbol；取不到传空串，venue 读败自然 unknown 不猜）。
		canon := c.CanonicalBuy
		if i == 1 {
			canon = c.CanonicalSell
		}
		sym := ""
		if s.qbus != nil {
			if q, ok := s.qbus.Latest(canon, vk); ok {
				sym = q.VenueSymbol
			}
		}
		frac, note, known := s.legMarginNeedFrac(ctx, vk, sym, i == 1)
		if !known {
			unknown = append(unknown, fmt.Sprintf("%s（%s）", vk, note))
			continue
		}
		if need*frac > avail {
			return "conflict", fmt.Sprintf("腿池保证金不足：%s 需 $%.0f（名义 $%.0f×率 %.3f）> 可用 $%.0f",
				vk, need*frac, need, frac, avail)
		}
	}
	if len(unknown) > 0 {
		return "unknown", "腿池杠杆数据缺，保证金需求待定：" + strings.Join(unknown, "；")
	}
	return "ok", ""
}
