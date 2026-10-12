package autotrade

// engine_opp_margin.go D-010 S3：margin_usd 预算→单腿名义折算 + canonical 聚簇闸
// 顺序面（自 engine_opp.go 折分，行数门禁）。

import (
	"context"
	"log"

	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// xassetMarginGate D-010 S3：margin 模式预算折算（N=margin÷(fracB+fracS)）+
// canonical 聚簇 cap 前瞻闸。返回（margin 折算出的单腿名义, ok）——
// ok=false=静默跳过已 log（杠杆实读失败 fail-closed / canonical 超档）；
// 非 margin 行 legN=0（调用方维持 notional 口径 sizing）。
func (e *Engine) xassetMarginGate(ctx context.Context, opp pgstore.Opportunity, sellCanonical string, spec *exec.XAssetComboAllowSpec) (float64, bool) {
	marginLegN := 0.0
	if spec.MarginUSD != nil {
		// margin_usd 预算→单腿名义 N=margin÷(fracB+fracS)；frac 实读缺一 →
		// fail-closed 跳过（杠杆实读失败保证金不可折算，不猜杠杆放行）。
		n, ok := e.marginLegNotional(ctx, opp, sellCanonical, spec)
		if !ok {
			log.Printf("[autotrade] skip %s: margin_usd 预算杠杆实读失败保证金不可折算（fail-closed 拒开）", opp.ID)
			return 0, false
		}
		marginLegN = n
	}
	// D-006 S3：canonical 聚簇 cap 前瞻闸（判据体在 engine_opp_capgate.go）。
	if e.canonicalCapRejectLog(ctx, opp, sellCanonical, spec, marginLegN) {
		return 0, false
	}
	return marginLegN, true
}

// marginLegNotional D-010 S3：margin_usd 预算→单腿名义 N=margin÷(fracB+fracS)。
// frac 经 engineService.LegMarginFrac 实读（venue 原生符号=qbus canonical→VenueSymbol
// 解析，同 dashboard comboPoolVerdict 法）；任一不可得→ok=false（fail-closed）。
func (e *Engine) marginLegNotional(ctx context.Context, opp pgstore.Opportunity, sellCanonical string, spec *exec.XAssetComboAllowSpec) (float64, bool) {
	if e.service == nil {
		return 0, false
	}
	symOf := func(canon, vk string) string {
		if e.quotes != nil {
			if q, ok := e.quotes.Latest(canon, vk); ok {
				return q.VenueSymbol
			}
		}
		return ""
	}
	fracB, okB := e.service.LegMarginFrac(ctx, spec.VenueBuy, symOf(opp.Canonical, spec.VenueBuy), false)
	fracS, okS := e.service.LegMarginFrac(ctx, spec.VenueSell, symOf(sellCanonical, spec.VenueSell), true)
	if !okB || !okS || fracB+fracS <= 0 {
		return 0, false
	}
	return *spec.MarginUSD / (fracB + fracS), true
}
