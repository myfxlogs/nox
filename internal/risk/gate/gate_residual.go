package gate

import (
	"context"
	"fmt"
)

// gate_residual.go —— D-015 ①（R1）：残腿闸。开仓门禁新增前置检查——
// 任一持仓处于 CLOSING∪LEGGED（残腿裸露窗口）→ 拒新开 fail-closed。
// 事故锚：#505 gate 腿裸露 2h23m、两 venue「平仓通道降级」告警中，14:58 仍放行 #506。
// R1 修订：venue 降级集合不进开仓判定——闸序上本检查只在 CLOSING∪LEGGED 为空时
// 到达，此时降级标记必为「残腿已平但标记未清」的陈旧态，消费之=永久误拦；
// 集合保留给 reconciler 告警/观测（DegradedVenues）。

// residualLegGate 残腿拒开。fail-closed：查询失败=拒绝（与检查 2-4 同风格）。
func (g *Gate) residualLegGate(ctx context.Context, intent *TradeIntent) (GateResult, bool) {
	closings, err := g.store.ListPositions(ctx, "CLOSING", 1, 0)
	if err != nil {
		g.rejectLog(ctx, *intent, fmt.Sprintf("list CLOSING: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "residual leg check failed"}, true
	}
	leggeds, err := g.store.ListPositions(ctx, "LEGGED", 1, 0)
	if err != nil {
		g.rejectLog(ctx, *intent, fmt.Sprintf("list LEGGED: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "residual leg check failed"}, true
	}
	if len(closings) > 0 || len(leggeds) > 0 {
		reason := fmt.Sprintf("residual leg: %d closing %d legged", len(closings), len(leggeds))
		g.rejectLog(ctx, *intent, reason, false)
		return GateResult{Status: "REJECTED", Reason: reason}, true
	}
	return GateResult{}, false
}
