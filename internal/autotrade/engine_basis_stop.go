// engine_basis_stop.go D-341 S2：XASSET 止损判距外化（自 engine_position.go 拆出保行预算）。
package autotrade

import (
	"fmt"

	"arb/internal/store/pgstore"
)

// basisStopCheckXAsset D-341 S2：XASSET 专用止损——判距外化 max(BasisStopBps, entry_noise_bps)
// （噪声内不止损；风险注记=单次止损额升至 ~noise×notional，换噪声内零误杀，S3 样本验证降幅）。
// entry_noise_bps NULL（历史行/学习锚缺）→ 退回 cfg.BasisStopBps（保守不放宽）。
// fmtStopBps S2 日志两值（cfg vs noise 外化后判距来源）。
func fmtStopBps(cfgBps float64, noise *float64) string {
	if noise != nil {
		return fmt.Sprintf("max(%.1f,noise %.1f)", cfgBps, *noise)
	}
	return fmt.Sprintf("cfg %.1f", cfgBps)
}

// basisStopCheck D-311 S5：基差止损块抽出共用（SWAPARB/CARRY 与 XASSET_CARRY 同语义）。
// D-180 S3：PnL_bps = currentBasisBps − expected_entry_spread_bps。
// PnL_bps < −BasisStopBps → 平仓（亏损方向，非旧 A5 的盈利方向绝对值门禁）。
// 口径：entry=成交价(含滑点) / current=报价口径(不含滑点) → PnL 保守(止损偏早=安全)。
// 基准列 NULL/报价缺失 → 不决策（fail-closed 惯例）。
// BasisStopBps<=0 → 回退旧 swapArbBasisExitBps 绝对值门禁（向后兼容）。
func (e *Engine) basisStopCheck(p *pgstore.Position) (bool, string) {
	if e.cfg.BasisStopBps > 0 {
		basisBps, bok := e.currentBasisBps(p)
		if shouldClose, reason := basisStopLossCheck(basisBps, bok, p.ExpectedEntrySpreadBps, e.cfg.BasisStopBps); shouldClose {
			return true, reason
		}
	} else {
		// D-108 A5 旧门禁（BasisStopBps<=0 时回退）：basisBps > swapArbBasisExitBps → 平仓。
		if basisBps, bok := e.currentBasisBps(p); bok {
			if basisBps > swapArbBasisExitBps {
				return true, fmt.Sprintf("basis loss limit %.1f bps > %.1f", basisBps, swapArbBasisExitBps)
			}
		}
	}
	return false, ""
}
