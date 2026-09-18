// service_carry_comparison.go D-341 S1：开仓学习样本锚写点 helper（ExecuteOpportunityInternal /
// MeanRevertWithCap 两写点共用，D-327 EnsureEntrySpreadFromLegs 同位）。
// monitor 缺行/未成噪 → 不写（positions.entry_noise_bps/entry_harvest_floor_bps NULL=诚实缺样本，
// S2 止损外化与 S1 收割出口均走 NULL 不触发/保守语义）。
package dashboard

import (
	"context"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// entryGateSnapshotMaxAge D-341 S1：开仓时点 monitor 行新鲜度（同 D-335 闸 30min 容差）。
const entryGateSnapshotMaxAge = 30 * time.Minute

// ensureEntryGateSnapshot D-341 S1：ExecuteOpportunityInternal 写点——
// pairKey=D-335 同款（CanonicalSell-TrimSuffix(Canonical,"USD")）。
func (s *Service) ensureEntryGateSnapshot(ctx context.Context, posID int64, found *pgstore.Opportunity) {
	sellCanon := found.Canonical
	if found.CanonicalSell != nil && *found.CanonicalSell != "" {
		sellCanon = *found.CanonicalSell
	}
	pairKey := sellCanon + "-" + strings.TrimSuffix(found.Canonical, "USD")
	_ = s.store.EnsureEntryGateSnapshot(ctx, posID, pairKey, entryGateSnapshotMaxAge)
}

// ensureEntryGateSnapshotCanonical D-341 S1：MeanRevertWithCap 写点（双腿同 canonical 兜底形态）。
func (s *Service) ensureEntryGateSnapshotCanonical(ctx context.Context, posID int64, canonical string) {
	_ = s.store.EnsureEntryGateSnapshot(ctx, posID, canonical+"-"+strings.TrimSuffix(canonical, "USD"), entryGateSnapshotMaxAge)
}
