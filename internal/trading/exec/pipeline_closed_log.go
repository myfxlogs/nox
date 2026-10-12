// pipeline_closed_log.go —— D-249 S1 第3项：CLOSED 翻译点记录（腿级摘要）。
// 裁定（a385b84）：closePositionCore 成功 CLOSED 后写一行 CLOSED log；
// 幂等=依赖 ErrTerminalState（禁预查 TOCTOU）；log 写失败 best-effort 不阻断（同 gate.go:263）。
package exec

import (
	"context"
	"fmt"
	"log"
	"strings"

	"arb/internal/store/pgstore"
)

// logClosed D-249 S1 第3项：CLOSED 翻译点记录（腿级摘要）。
// reason=`closed realized_pnl=X legs=[venue:ticket,...]`（venue:ticket 取 CLOSE FILLED legs）。
// 只填手头已有字段（pos/legs/totalPnL），禁为填列加查询（裁定附条）。
// best-effort：写失败只 log 不阻断（权威记录在 positions 表 status/realized_pnl/closed_at）。
func (p *Pipeline) logClosed(ctx context.Context, pos *pgstore.Position, closeLegs []pgstore.PositionLeg, totalPnL float64) {
	var parts []string
	for _, l := range closeLegs {
		if l.Status == "FILLED" {
			parts = append(parts, l.Venue+":"+l.OrderID)
		}
	}
	pid := pos.ID
	reason := fmt.Sprintf("closed realized_pnl=%.2f legs=[%s]", totalPnL, strings.Join(parts, ","))
	if err := p.store.InsertAutotradeLog(ctx, &pgstore.AutotradeLog{
		OpportunityID: pos.OpportunityID,
		PositionID:    &pid,
		Strategy:      pos.Type,
		Status:        "CLOSED",
		Reason:        reason,
	}); err != nil {
		log.Printf("[exec] D-249 CLOSED log insert failed pos=%d (best-effort, 权威在 positions 表): %v", pid, err)
	}
}
