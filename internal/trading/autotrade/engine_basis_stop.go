// engine_basis_stop.go D-341 S2：XASSET 止损判距外化（自 engine_position.go 拆出保行预算）。
package autotrade

import (
	"context"
	"fmt"

	"nox/internal/store/pgstore"
)

// basisStopCheckXAsset D-378b 撤除（窄阈止损改灾难带，见 engine_disaster.go）。
// fmtStopBps 同删（零调用方——XASSET 观测行 detail 改用 fmtDisasterBand）。

// basisStopCheck D-311 S5：基差止损块抽出共用（SWAPARB/CARRY 与 XASSET_CARRY 同语义）。
// PnL_bps = currentBasisBps − expected_entry_spread_bps。
// PnL_bps < −BasisStopBps → 平仓（亏损方向，非旧 A5 的盈利方向绝对值门禁）。
// 口径：entry=成交价(含滑点) / current=报价口径(不含滑点) → PnL 保守(止损偏早=安全)。
// 基准列 NULL/报价缺失 → 不决策（fail-closed 惯例）。
// BasisStopBps<=0 → 回退旧 swapArbBasisExitBps 绝对值门禁（向后兼容）。
func (e *Engine) basisStopCheck(p *pgstore.Position) (bool, string) {
	legs, err := e.store.ListPositionLegs(context.Background(), p.ID)
	if err != nil {
		return false, ""
	}
	return e.basisStopCheckLegs(p, legs)
}

// basisStopCheckLegs D-004 P1#4：legs 外给变体（quote 快路零 DB 复用缓存腿）。
// legs 缺失/双腿不齐 → 不决策（fail-closed，同 currentBasisBps 原路径）。
func (e *Engine) basisStopCheckLegs(p *pgstore.Position, legs []pgstore.PositionLeg) (bool, string) {
	basisBps, bok := e.currentBasisBpsLegs(p, legs)
	return e.basisStopDecision(p, basisBps, bok)
}

// basisStopCheckResolved D-004 P1#4：sellCanonical 已解析变体——quote 快路用
// openEntry.sellCanonical 缓存，全程零 DB。
func (e *Engine) basisStopCheckResolved(p *pgstore.Position, legs []pgstore.PositionLeg, sellCanonical string) (bool, string) {
	basisBps, bok := e.currentBasisBpsLegsResolved(p, legs, sellCanonical)
	return e.basisStopDecision(p, basisBps, bok)
}

// basisStopDecision 阈值分支单源（BasisStopBps>0 止损 / <=0 旧绝对值门禁回退）。
func (e *Engine) basisStopDecision(p *pgstore.Position, basisBps float64, bok bool) (bool, string) {
	if e.cfg.BasisStopBps > 0 {
		if shouldClose, reason := basisStopLossCheck(basisBps, bok, p.ExpectedEntrySpreadBps, e.cfg.BasisStopBps); shouldClose {
			return true, reason
		}
	} else {
		// 旧门禁（BasisStopBps<=0 时回退）：basisBps > swapArbBasisExitBps → 平仓。
		if bok && basisBps > swapArbBasisExitBps {
			return true, fmt.Sprintf("basis loss limit %.1f bps > %.1f", basisBps, swapArbBasisExitBps)
		}
	}
	return false, ""
}
