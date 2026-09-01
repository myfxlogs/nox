// D-180 S3：基差止损翻正 + entry 基准写入。
// expected_entry_spread_bps = (buyActual − sellActual) / buyActual × 10000（双腿成交价差）。
// 止损条件：PnL_bps = currentBasisBps − expected_entry_spread_bps < −ARB_SWAPARB_BASIS_STOP_BPS → 平仓。
// 口径：entry=成交价(含滑点) / current=报价口径(不含滑点) → PnL 保守(止损偏早=安全)。
package autotrade

import (
	"context"
	"fmt"

	"arb/internal/store/pgstore"
)

// computeEntrySpreadFromLegs D-180 S3：从 position_legs 取双腿 price_actual 算 entry spread bps。
// = (buyActual − sellActual) / buyActual × 10000（BUY 成交=ask、SELL 成交=bid）。
// 任一腿 price_actual nil 或 buyActual<=0 → ok=false（fail-closed 不写基准）。
func computeEntrySpreadFromLegs(ctx context.Context, store *pgstore.Store, posID int64) (float64, bool) {
	legs, err := store.ListPositionLegs(ctx, posID)
	if err != nil {
		return 0, false
	}
	var buyLeg, sellLeg *pgstore.PositionLeg
	for i := range legs {
		if legs[i].Phase != "OPEN" {
			continue
		}
		if legs[i].Side == "BUY" {
			buyLeg = &legs[i]
		} else {
			sellLeg = &legs[i]
		}
	}
	if buyLeg == nil || sellLeg == nil {
		return 0, false
	}
	if buyLeg.PriceActual == nil || sellLeg.PriceActual == nil {
		return 0, false
	}
	buyActual := *buyLeg.PriceActual
	sellActual := *sellLeg.PriceActual
	if buyActual <= 0 {
		return 0, false
	}
	return (buyActual - sellActual) / buyActual * 10000, true
}

// basisStopLossCheck D-180 S3：基差止损翻正——PnL_bps = currentBasisBps − expected_entry_spread_bps。
// PnL_bps < −BasisStopBps → 平仓（reason 含 entry/now 值）。
// 口径：entry=成交价(含滑点) / current=报价口径(不含滑点) → PnL 保守(止损偏早=安全)。
// 基准列 NULL（ExpectedEntrySpreadBps nil）→ 不决策（fail-closed）。
// 报价缺失（currentOk=false）→ 不决策（fail-closed）。
// BasisStopBps<=0 → 不校验（向后兼容，回退旧 swapArbBasisExitBps 绝对值门禁）。
func basisStopLossCheck(currentBasisBps float64, currentOk bool, entrySpreadBps *float64, basisStopBps float64) (bool, string) {
	if basisStopBps <= 0 {
		return false, ""
	}
	if !currentOk || entrySpreadBps == nil {
		return false, ""
	}
	pnlBps := currentBasisBps - *entrySpreadBps
	if pnlBps < -basisStopBps {
		return true, fmt.Sprintf("basis loss %.1f bps (entry %.1f → now %.1f)", pnlBps, *entrySpreadBps, currentBasisBps)
	}
	return false, ""
}

// backfillEntrySpread D-180 S3：存量活跃仓 entry 基准回填。
// 动态查全部活跃仓（status IN OPEN/OPENING/LEGGED/CLOSING），禁写死 position_id。
// 同公式 computeEntrySpreadFromLegs；UpdateExpectedEntrySpreadBps 幂等（已有值不覆写）。
func (e *Engine) backfillEntrySpread(ctx context.Context) {
	if e.store == nil {
		return
	}
	positions, err := e.store.ListActivePositions(ctx)
	if err != nil {
		return
	}
	for _, p := range positions {
		if p.Type != "SWAPARB" && p.Type != "CARRY" {
			continue
		}
		if p.ExpectedEntrySpreadBps != nil {
			continue // 已有基准，跳过
		}
		entryBps, ok := computeEntrySpreadFromLegs(ctx, e.store, p.ID)
		if !ok {
			continue // 缺成交价，跳过
		}
		_ = e.store.UpdateExpectedEntrySpreadBps(ctx, p.ID, entryBps)
	}
}

// BackfillEntrySpreadPublic D-180 S3：boot 存量回填公开入口（cmd/core 调用）。
func (e *Engine) BackfillEntrySpreadPublic(ctx context.Context) {
	e.backfillEntrySpread(ctx)
}
