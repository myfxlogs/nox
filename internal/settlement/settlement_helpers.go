// settlement_helpers.go —— Settle 兼容包装 + hasCashflowKinds。
// 从 settlement.go 拆出保 300 软线——行数治理纯搬家，零逻辑变更。
package settlement

import (
	"context"
	"time"
)

// Settle 兼容包装（D-194 §3.3：生产旧入口 = SettleAt(..., time.Now())）。
func Settle(ctx context.Context, deps Deps, posID int64, elim Eliminator, family FamilyResolver) error {
	return SettleAt(ctx, deps, posID, elim, family, time.Now())
}

// hasCashflowKinds D-193-A S7：判断该 position 是否应有现金流（SWAPARB/CARRY/FUNDING_HEDGE/PERP_FUNDING
// 有 swap/funding；D-331 +XASSET_CARRY——perp 腿 funding 现金流缺失必须判 unknown→retryable，不静默 skipped）。
// CROSS_VENUE/MEANREVERT 无 swap/funding 现金流 → no_cashflows 是正常状态，不判 unknown。
func hasCashflowKinds(posType string) bool {
	switch posType {
	case "SWAPARB", "CARRY", "FUNDING_HEDGE", "PERP_FUNDING", "XASSET_CARRY":
		return true
	default:
		return false
	}
}
