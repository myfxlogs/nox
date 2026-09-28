// service_graduation_alloc_risk.go D-540：毕业授权可用基准/风险位/建议额评估
// （自 sizing.go 析出——池快照行/可用基准求值/腿保证金率/风险位与安全垫反推。
// 零信任：一切阈值与安全线由服务端裁定，前端只渲染三字段）。
package dashboard

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
)

// allocPool 腿资金池快照行（equity 原币+currency+fetched_at；D-540 补 free_margin）。
// D-542：live+src——实拉活账本/REST 校准池（live=true）免快照龄闸；src 供池行/基准注记透出。
type allocPool struct {
	key      string // 实际取到的 venue:venue_type 行键（unified 回退时如实反映口径）
	equity   float64
	free     *float64 // free_margin_raw；nil=未采→可用回退 equity
	currency string
	at       time.Time
	live     bool   // D-542：实拉活账本/REST 校准源（非快照）
	src      string // "ws"=私有流 / "rest"=实拉校准 / ""=快照兜底
}

// allocPoolOf 腿 venue_key→资金池快照行（SSOT §2 poolOf）：先精确 (venue,冒号后缀type)
// 行；缺行/NULL equity 回退 (venue,unified)→(venue,perp)→该 venue 任意非空 equity 行；
// 仍无=未采（false）。同所双池语义：spot×perp 组合两腿各取各自池行（现货花 spot 现金，
// 永续占 perp 保证金）——bare venue 折叠是漏行（D-358fix 同判）。
func allocPoolOf(snaps []pgstore.VenueAccountLatest, venueKey string) (allocPool, bool) {
	k := keydomain.Parse(venueKey)
	bare, vt := k.Venue, k.Type
	pick := func(wantType string) (allocPool, bool) {
		for _, r := range snaps {
			if r.Venue == bare && r.VenueType == wantType && r.EquityRaw != nil {
				return allocPool{key: keydomain.Join(bare, r.VenueType), equity: *r.EquityRaw, free: r.FreeMarginRaw, currency: r.Currency, at: r.FetchedAt}, true
			}
		}
		return allocPool{}, false
	}
	for _, t := range []string{vt, "unified", "perp"} {
		if p, ok := pick(t); ok {
			return p, true
		}
	}
	for _, r := range snaps {
		if r.Venue == bare && r.EquityRaw != nil {
			return allocPool{key: keydomain.Join(bare, r.VenueType), equity: *r.EquityRaw, free: r.FreeMarginRaw, currency: r.Currency, at: r.FetchedAt}, true
		}
	}
	return allocPool{}, false
}

// allocUsable 池可用资金：free_margin 优先（venue 实采可用口径），缺 free → equity 回退。
// 返回 (可用值, free实采?——false=净值回退，注记须标)。
func allocUsable(p allocPool) (float64, bool) {
	if p.free != nil {
		return *p.free, true
	}
	return p.equity, false
}

// allocBaseEval 双池可用基准（D-540）：min(两池可用)；返回基准/绑定池键/来源注记
// （逐池标 free_margin|净值回退——业主令 % 基准=可用资金非净值）。
func allocBaseEval(poolBuy, poolSell allocPool) (base float64, bindKey, note string) {
	uB, fB := allocUsable(poolBuy)
	uS, fS := allocUsable(poolSell)
	base, bind := uB, poolBuy
	if uS < uB {
		base, bind = uS, poolSell
	}
	src := func(p allocPool, f bool) string {
		if p.live {
			if p.src == "rest" {
				return "实拉校准"
			}
			return "实拉"
		}
		if f {
			return "free_margin"
		}
		return "净值回退"
	}
	return base, bind.key, fmt.Sprintf("%s=%s %s=%s；绑定 %s", poolBuy.key, src(poolBuy, fB), poolSell.key, src(poolSell, fS), bind.key)
}

// allocPoolUSDLike 池币种白名单（SSOT §3-4：UST/USDT→USD=1 既定口径，其他币种不猜汇率）。
func allocPoolUSDLike(ccy string) bool {
	switch strings.ToUpper(strings.TrimSpace(ccy)) {
	case "USD", "USDT", "UST":
		return true
	}
	return false
}

// legMarginFrac 腿保证金率口径（D-540，与 risk 报告 leg 资金需求同口径）：
// MT=init_margin_rate 实采（缺→1/5 估+注记）/perp·unified=1/5/其他（spot 等非保证金腿）=名义全款。
func (s *Service) legMarginFrac(ctx context.Context, venueKey string) (float64, string) {
	k := keydomain.Parse(venueKey)
	switch k.Type {
	case "mt4", "mt5":
		fs, err := s.gradDB().LatestFacts(ctx, fact.KindInitMarginRate, k.Venue, "")
		if err == nil && len(fs) > 0 && fs[0].Value > 0 {
			return fs[0].Value, ""
		}
		return 0.2, "init_margin_rate 未采按 5x 估"
	case "perp", "unified":
		return 0.2, ""
	default:
		return 1.0, "非保证金腿按名义全款"
	}
}

// allocRisk D-540 风险位判定（红字数据源，不拦截——提交校验仍走原闸链）：
// insufficient=请求额>可用基准（suggested=基准全额）；liquidation=保证金估后池缓冲比
// equity/(已占保证金+腿保证金估) <marginUrgentMultiple（margin_guard urgent 同口径安全垫，
// 不新造阈值；已占=equity−free_margin，free 缺=0）——suggested=缓冲反推∩基准。
// 同池双腿合并占用（两腿落同 (venue,type) 池行时保证金相加再比）。
func (s *Service) allocRisk(ctx context.Context, res allocSizing, venueBuy, venueSell string, poolBuy, poolSell allocPool) (string, []string, float64) {
	if res.AllocUSD > res.AllocBaseUSD {
		return "insufficient", []string{
			fmt.Sprintf("请求 $%.0f 超可用基准 $%.0f（%s）", res.AllocUSD, res.AllocBaseUSD, res.AllocBaseNote),
		}, res.AllocBaseUSD
	}
	fracByPool := map[string]float64{}
	poolByKey := map[string]allocPool{poolBuy.key: poolBuy, poolSell.key: poolSell}
	var reasons []string
	for i, vk := range []string{venueBuy, venueSell} {
		frac, basis := s.legMarginFrac(ctx, vk)
		pk := poolBuy.key
		if i == 1 {
			pk = poolSell.key // 池按 venueBuy/venueSell 位次绑定（pools[vk] 同序，同键自然合并）
		}
		fracByPool[pk] += frac
		if basis != "" {
			reasons = append(reasons, fmt.Sprintf("%s 保证金口径：%s", vk, basis))
		}
	}
	level := "ok"
	suggested := math.Inf(1)
	for pk, frac := range fracByPool {
		if frac <= 0 {
			continue
		}
		p := poolByKey[pk]
		marginEst := res.LegNotional * frac
		if marginEst <= 0 {
			continue
		}
		used := 0.0
		if p.free != nil {
			used = p.equity - *p.free // 已占保证金（缓冲语义=equity/MMR 同 margin_guard 族）
			if used < 0 {
				used = 0
			}
		}
		if headroom := p.equity / (used + marginEst); headroom < marginUrgentMultiple {
			level = "liquidation"
			reasons = append(reasons, fmt.Sprintf(
				"%s 池净值 $%.0f 已占 $%.0f，本次保证金估 $%.0f（名义 $%.0f×率 %.3g）→ 缓冲比 %.2f<%.1f 安全垫",
				pk, p.equity, used, marginEst, res.LegNotional, frac, headroom, marginUrgentMultiple))
		}
		if am := 2 * (p.equity/marginUrgentMultiple - used) / frac; am < suggested {
			suggested = am // 缓冲反推：alloc=2×legNotional，marginEst=legNotional×frac
		}
	}
	if level != "liquidation" {
		return level, reasons, 0
	}
	if suggested < 0 {
		suggested = 0
	}
	return level, reasons, math.Min(suggested, res.AllocBaseUSD)
}
