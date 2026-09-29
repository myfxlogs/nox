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
// MT=init_margin_rate fact（D-099 事件采集；≤24h 新鲜，缺/陈=unknown）；
// gate:perp=1/目标杠杆（S1 配置=推送条款）；其他 CEX=杠杆读不到→unknown；
// 非保证金腿（spot 等）=名义全款 1.0。
func (s *Service) legMarginNeedFrac(ctx context.Context, venueKey string) (frac float64, note string, known bool) {
	k := keydomain.Parse(venueKey)
	switch k.Type {
	case "mt4", "mt5":
		fs, err := s.gradDB().LatestFacts(ctx, fact.KindInitMarginRate, k.Venue, "")
		if err == nil && len(fs) > 0 && fs[0].Value > 0 && time.Since(fs[0].Ts) <= 24*time.Hour {
			return fs[0].Value, "", true
		}
		return 0, "init_margin_rate 未采或超 24h", false
	case "perp":
		// D-564 S1 U2 退役：gate 保证金率不再吃配置冒充（ARB_GATE_DEFAULT_LEVERAGE 链
		// 全删）——venue 实况 get_leverage 是执行链专责读，管理判据如实标缺（fail-closed
		// 待定，不冒充）。真金杠杆判定唯一权威=precheck 实时读。
		return 0, "gate 保证金率=venue 实况（get_leverage 执行链实读），管理判据不冒充", false
	case "unified":
		return 0, "unified 杠杆读不到", false
	default:
		return 1.0, "", true
	}
}

// comboPoolVerdict 组合腿池判定：need=腿名义×保证金率 vs 池可用。
// 腿名义=NotionalUSD（缺省回 MaxGrossUSD/2，与旧判据同基数）。
func (s *Service) comboPoolVerdict(ctx context.Context, c pgstore.XAssetComboAdminRow, snaps []pgstore.VenueAccountLatest) (verdict, detail string) {
	need := c.MaxGrossUSD / 2
	if c.NotionalUSD != nil {
		need = *c.NotionalUSD
	}
	var unknown []string
	for _, vk := range [2]string{c.VenueBuy, c.VenueSell} {
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
		frac, note, known := s.legMarginNeedFrac(ctx, vk)
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
