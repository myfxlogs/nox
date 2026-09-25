package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"

	"arb/internal/store/pgstore"
)

// comboAllowGate D-159 W1a R1 精确组合门禁（SWAPARB/CARRY 共用）。D-479 S1 自 gate.go
// Submit 整段搬移（纯搬移零逻辑变更）；返回值 rejected=true 时 res=拒绝结果。
func (g *Gate) comboAllowGate(ctx context.Context, intent *TradeIntent, activePositions []pgstore.Position) (GateResult, bool) {
	if intent.Strategy == "SWAPARB" || intent.Strategy == "CARRY" {
		// D-179 S3：预扫活跃 SWAPARB/CARRY 仓 notional_usd NULL（combo_allow.go 纯函数不打日志，warn 在此调用点）。
		for _, p := range activePositions {
			if p.OpportunityID != "" && (strings.HasPrefix(p.OpportunityID, "SWAPARB:") || strings.HasPrefix(p.OpportunityID, "CARRY:")) && p.NotionalUSD == nil {
				log.Printf("[autotrade] WARN position %d notional_usd NULL (backfill missing?) counted as 0", p.ID)
			}
		}
		cv := comboAllowVerdict(g.cfg.SwapArbAllowCombos, g.cfg.SwapArbDenyCombos, g.cfg.SwapArbAutoAllow, g.cfg.MaxActiveCombos, g.cfg.SwapArbMaxGrossExposure, *intent, activePositions)
		if !cv.Pass {
			g.rejectLog(ctx, *intent, cv.Reason, false)
			return GateResult{Status: "REJECTED", Reason: cv.Reason}, true
		}
		// D-168 超限折分：AdjustedNotional>0 表示需按剩余额度折分 qty
		// D-179 S4：折分 qty 向下规整到 0.01 步长（FX min lot），<0.01 拒开（宁可不开不可超限）。
		if cv.AdjustedNotional > 0 && intent.NotionalUSD > 0 {
			ratio := cv.AdjustedNotional / intent.NotionalUSD
			originalQty := intent.Qty
			originalNotional := intent.NotionalUSD
			splitQty := intent.Qty * ratio
			flooredQty, reject := grossSplitFloor(splitQty)
			if reject {
				reason := fmt.Sprintf("gross adjusted qty %.4f below min lot floor", splitQty)
				g.rejectLog(ctx, *intent, reason, false)
				return GateResult{Status: "REJECTED", Reason: reason}, true
			}
			intent.Qty = flooredQty
			// 重算 NotionalUSD 保持量价一致（规整后 qty 对应名义）
			intent.NotionalUSD = cv.AdjustedNotional * flooredQty / splitQty
			log.Printf("[autotrade] gate gross split: %s qty %.4f → %.4f, notional $%.2f → $%.2f (%s)",
				intent.DedupKey, originalQty, intent.Qty, originalNotional, intent.NotionalUSD, cv.Reason)
		}
	}
	return GateResult{}, false
}
