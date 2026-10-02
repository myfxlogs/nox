// engine_opp_capgate.go —— D-006 S3：canonical 聚簇 cap 前瞻闸（handleOpportunity 抽出，
// engine_opp.go 行数拆分）。与 pending 闸 6.5/建库事务复查（CreatePositionWithCapGuard）同判据。
package autotrade

import (
	"context"
	"log"

	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// canonicalCapRejectLog Σ在途同 canonical 名义+本次名义>cap → log skip（true=拦）。
// 静默 skip 不落 REJECTED（同白名单先例）。proposed=spec NotionalUSD→margin 反推 N→
// MaxGrossUSD 口径（D-010：canonical 聚簇 cap 保名义=裸腿风险域，margin 预算先折算
// 单腿名义再判）。
func (e *Engine) canonicalCapRejectLog(ctx context.Context, opp pgstore.Opportunity, sellCanonical string, spec *exec.XAssetComboAllowSpec, legNotional float64) bool {
	proposed := spec.MaxGrossUSD
	if spec.NotionalUSD != nil {
		proposed = *spec.NotionalUSD
	}
	if legNotional > 0 {
		proposed = legNotional
	}
	canons := []string{opp.Canonical}
	if sellCanonical != opp.Canonical {
		canons = append(canons, sellCanonical)
	}
	for _, canon := range canons {
		if r := e.store.CanonicalCapReject(ctx, canon, proposed); r != "" {
			log.Printf("[autotrade] skip %s: %s", opp.ID, r)
			return true
		}
	}
	return false
}
