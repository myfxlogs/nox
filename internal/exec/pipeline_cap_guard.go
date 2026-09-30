// pipeline_cap_guard.go —— D-006 S3：canonical 聚簇 cap 建库守卫的入参推定。
// canonicals/proposed 由 opp 行 + 白名单 spec 推定；opp 缺失 fail-closed（不建仓）。
package exec

import (
	"context"
	"fmt"
)

// capGuardInputs 返回守卫建库的 canonical 集 + 本次名义（USD）。
// 非 XASSET_CARRY/FUNDING_HEDGE 型返回 nil（不启用守卫通道）。
// proposed 口径：白名单 spec NotionalUSD→MaxGrossUSD→双腿 gross 兜底。
func (p *Pipeline) capGuardInputs(ctx context.Context, posType, opportunityID string, legs []LegSpec) ([]string, float64, error) {
	if posType != "XASSET_CARRY" && posType != "FUNDING_HEDGE" {
		return nil, 0, nil
	}
	opp, oerr := p.store.GetOpportunity(ctx, opportunityID)
	if oerr != nil || opp == nil {
		return nil, 0, fmt.Errorf("canonical cap: opportunity %q lookup failed (fail-closed, err=%v)", opportunityID, oerr)
	}
	canonicalSell := opp.Canonical
	if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		canonicalSell = *opp.CanonicalSell
	}
	canonicals := []string{opp.Canonical, canonicalSell}
	var proposed float64
	if spec := MatchXAssetComboAllow(p.xassetAllowSpecs(), opp.Canonical, canonicalSell, opp.VenueBuy, opp.VenueSell); spec != nil {
		proposed = spec.MaxGrossUSD
		if spec.NotionalUSD != nil {
			proposed = *spec.NotionalUSD
		}
	}
	if proposed <= 0 {
		for _, leg := range legs {
			proposed += leg.Qty * leg.PriceExpected
		}
	}
	return canonicals, proposed, nil
}
