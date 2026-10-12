// pipeline_cap_guard.go —— D-006 S3：canonical 聚簇 cap 建库守卫的入参推定。
// canonicals/proposed 由 opp 行 + 白名单 spec 推定；opp 缺失 fail-closed（不建仓）。
package exec

import (
	"context"
	"fmt"
)

// capGuardInputs 返回守卫建库的 canonical 集 + 本仓腿 venue→计划保证金 map + 本次名义（USD）。
// 非 XASSET_CARRY/FUNDING_HEDGE 型返回 nil（不启用守卫通道）。
// proposed 口径：白名单 spec NotionalUSD→margin_usd 名义反推→MaxGrossUSD→双腿 gross 兜底
// （canonical 聚簇 cap 保名义口径=裸腿风险域）。
// venueMargins D-010：venue cap 翻保证金占用预算后，本仓该 venue 腿保证金=N×frac(v)；
// frac 实读缝未接线/读败 → 该腿按名义全款计（frac=1 保守不放行）。
func (p *Pipeline) capGuardInputs(ctx context.Context, posType, opportunityID string, legs []LegSpec) ([]string, map[string]float64, float64, error) {
	if posType != "XASSET_CARRY" && posType != "FUNDING_HEDGE" {
		return nil, nil, 0, nil
	}
	opp, oerr := p.store.GetOpportunity(ctx, opportunityID)
	if oerr != nil || opp == nil {
		return nil, nil, 0, fmt.Errorf("canonical cap: opportunity %q lookup failed (fail-closed, err=%v)", opportunityID, oerr)
	}
	canonicalSell := opp.Canonical
	if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		canonicalSell = *opp.CanonicalSell
	}
	canonicals := []string{opp.Canonical, canonicalSell}
	var proposed float64
	proposedIsLegN := false // proposed 已是单腿名义口径（notional_usd 或 margin 反推 N）
	var marginUSD *float64
	if spec := MatchXAssetComboAllow(p.xassetAllowSpecs(), opp.Canonical, canonicalSell, opp.VenueBuy, opp.VenueSell); spec != nil {
		proposed = spec.MaxGrossUSD
		if spec.NotionalUSD != nil {
			proposed = *spec.NotionalUSD
			proposedIsLegN = true
		}
		marginUSD = spec.MarginUSD
	}
	if proposed <= 0 && marginUSD == nil {
		for _, leg := range legs {
			proposed += leg.Qty * leg.PriceExpected
		}
	}
	// venue 判据键集=本仓腿 venue_key（typed；fin_venue_exposure_caps 同域），
	// 值=该腿计划保证金=N×frac(v)。frac 读败/未接线 → frac=1 名义全款计（保守）。
	// margin_usd 模式下 N=margin÷(fracB+fracS)——任一 frac 实读不可得 → fail-closed
	// 拒建仓（杠杆实读失败保证金不可折算，不猜）。
	fracOf := func(leg LegSpec) (float64, bool) {
		if p.marginFracFor == nil {
			return 1.0, false
		}
		if f, known := p.marginFracFor(ctx, leg.VenueKey, leg.VenueSymbol, leg.Side == "SELL"); known && f > 0 {
			return f, true
		}
		return 1.0, false
	}
	var fracB, fracS float64
	knownB, knownS := false, false
	for _, leg := range legs {
		f, known := fracOf(leg)
		if leg.Side == "BUY" {
			fracB, knownB = f, known
		} else if leg.Side == "SELL" {
			fracS, knownS = f, known
		}
	}
	if marginUSD != nil {
		if !knownB || !knownS {
			return nil, nil, 0, fmt.Errorf("margin 模式腿保证金率不可得（杠杆实读失败保证金不可折算，fail-closed）")
		}
		proposed = *marginUSD / (fracB + fracS) // N=单腿名义（名义相等对冲）
		proposedIsLegN = true
	}
	legN := proposed
	if !proposedIsLegN && len(legs) > 0 {
		legN = proposed / 2 // gross 兜底口径=双腿合计→单腿名义取半（与 notional 单腿口径对齐）
	}
	margins := map[string]float64{}
	for _, leg := range legs {
		if leg.VenueKey == "" {
			continue
		}
		f, _ := fracOf(leg)
		margins[leg.VenueKey] += legN * f // 同 venue 双腿（同所对冲）累加
	}
	return canonicals, margins, proposed, nil
}
