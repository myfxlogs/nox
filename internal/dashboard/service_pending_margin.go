// service_pending_margin.go D-010：保证金口径派生链——单腿名义 N 反推 + 腿保证金率
// 实读封装 + qty 下限保证金化（S0 2×误拦回归修复随本文件重写）。
// 判据单源（P3）：待入场闸（comboQtyBelowMin/exposureCapsGate）、池判定
// （comboPoolVerdict）、approve 闸（exposureCapGate）、前端透显字段同调本文件函数。
package dashboard

import (
	"context"
	"fmt"
	"math"

	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
	"arb/internal/usdnotional"
)

// legMarginFracFor 腿保证金率解析（展示/闸/守卫三面同源）：venue 原生符号经 qbus
// canonical→VenueSymbol 解析（与 comboPoolVerdict 同法）；frac 实读（legMarginNeedFrac
// 链：MT=init_margin_rate fact 悲观档/perp=杠杆实读/spot=1.0）；未知→known=false。
func (s *Service) legMarginFracFor(ctx context.Context, venueKey, canonical string, sellLeg bool) (float64, bool) {
	sym := ""
	if s.qbus != nil {
		if q, ok := s.qbus.Latest(canonical, venueKey); ok {
			sym = q.VenueSymbol
		}
	}
	f, _, known := s.legMarginNeedFrac(ctx, venueKey, sym, sellLeg)
	return f, known
}

// comboLegNotional D-010 S3：组合单腿名义 N + 双腿保证金率。
// margin_usd 模式：N=margin÷(fracB+fracS)——名义相等对冲，保证金按各腿杠杆反推不等
// 份额；任一 frac 实读不可得 → ok=false（杠杆实读失败保证金不可折算，fail-closed）。
// 名义模式：N=notional_usd（NULL→MaxGrossUSD/2 双腿口径取半）。
func (s *Service) comboLegNotional(ctx context.Context, c pgstore.XAssetComboAdminRow) (n, fracB, fracS float64, knownB, knownS, ok bool) {
	fracB, knownB = s.legMarginFracFor(ctx, c.VenueBuy, c.CanonicalBuy, false)
	fracS, knownS = s.legMarginFracFor(ctx, c.VenueSell, c.CanonicalSell, true)
	if c.MarginUSD != nil {
		if !knownB || !knownS || fracB+fracS <= 0 {
			return 0, fracB, fracS, knownB, knownS, false
		}
		return *c.MarginUSD / (fracB + fracS), fracB, fracS, true, true, true
	}
	n = c.MaxGrossUSD / 2
	if c.NotionalUSD != nil {
		n = *c.NotionalUSD
	}
	return n, fracB, fracS, knownB, knownS, n > 0
}

// comboQtyBelowMin D-009 S4→D-010 S0：qty 明示（S0 修 2× 误拦回归——notional_usd=
// 单腿名义口径，判据=N≥max(minB,minS)，原 2×max 把可开组合误拦）。
// margin_usd 模式（S3）：minMargin=max(minB,minS)×(fracB+fracS)，文案「最少需保证金」；
// frac 实读失败→拦（保证金不可折算 fail-closed）。
// 缺失输入（无报价/无 step 元数据）不阻塞——其余闸（tick/monitor 新鲜度）已各自把守。
// qtyStepForVenue 取自 service_execute_meanrevert.go floorToStepQtyMin/cexStepSize 同源。
func (s *Service) comboQtyBelowMin(ctx context.Context, c pgstore.XAssetComboAdminRow) (string, bool) {
	if c.MaxLongQty > 0 || c.MaxShortQty > 0 { // D-330 既有覆盖语义保真：MAX-qty 覆盖不再按名义测
		return "", false
	}
	minB, okB := s.minOpenableUSD(c.VenueBuy, c.CanonicalBuy, "BUY")
	minS, okS := s.minOpenableUSD(c.VenueSell, c.CanonicalSell, "SELL")
	if !okB || !okS {
		return "", false
	}
	minN := math.Max(minB, minS)
	if c.MarginUSD != nil {
		n, fracB, fracS, _, _, ok := s.comboLegNotional(ctx, c)
		if !ok {
			return "杠杆实读失败保证金不可折算（margin 预算需双腿杠杆实读）", true
		}
		_ = n // N 透出由 pendingRow 字段承担
		minMargin := minN * (fracB + fracS)
		if *c.MarginUSD >= minMargin {
			return "", false
		}
		return fmt.Sprintf("该组合最少需保证金 $%.0f，当前 $%.0f，差 $%.0f（场地最小步进）", minMargin, *c.MarginUSD, minMargin-*c.MarginUSD), true
	}
	proposed := c.MaxGrossUSD / 2
	if c.NotionalUSD != nil {
		proposed = *c.NotionalUSD
	}
	if proposed <= 0 {
		return "", false
	}
	if proposed >= minN {
		return "", false
	}
	return fmt.Sprintf("该组合最小可开单腿名义 $%.0f，当前 $%.0f，差 $%.0f（场地最小步进）", minN, proposed, minN-proposed), true
}

// minOpenableUSD 单腿最小开单名义=minStep(1 步)×perLotUSD（qty 明示的 USD 锚）。
// 缺报价/cs/step 元数据 → ok=false（不猜）。
func (s *Service) minOpenableUSD(venueKey, canonical, side string) (float64, bool) {
	if s.qbus == nil {
		return 0, false
	}
	q, ok := s.qbus.Latest(canonical, venueKey)
	if !ok || q.Bid <= 0 || q.Ask <= 0 {
		return 0, false
	}
	px := q.Ask
	if side == "SELL" {
		px = q.Bid
	}
	cs, err := s.legContractSizeOf(venueKey, q.VenueSymbol)
	if err != nil {
		return 0, false
	}
	perLot, ok := usdnotional.PerLot(canonical, px, cs, s.qbus)
	if !ok {
		return 0, false
	}
	minStep := s.cexStepSize(q.VenueSymbol)
	if keydomain.IsMTKey(venueKey) {
		minStep = s.floorToStepQtyMin(venueKey, q.VenueSymbol)
	}
	if perLot <= 0 || minStep <= 0 {
		return 0, false
	}
	return minStep * perLot, true
}

// comboMinMarginUSD 组合最少需保证金=max(双腿最小可开名义)×(fracB+fracS)——
// 展示透显用（pending 行 min_margin_usd 字段）；任一输入缺 → ok=false 留空不猜。
func (s *Service) comboMinMarginUSD(ctx context.Context, c pgstore.XAssetComboAdminRow) (float64, bool) {
	minB, okB := s.minOpenableUSD(c.VenueBuy, c.CanonicalBuy, "BUY")
	minS, okS := s.minOpenableUSD(c.VenueSell, c.CanonicalSell, "SELL")
	_, fracB, fracS, kB, kS, _ := s.comboLegNotional(ctx, c)
	if !okB || !okS || !kB || !kS {
		return 0, false
	}
	return math.Max(minB, minS) * (fracB + fracS), true
}
