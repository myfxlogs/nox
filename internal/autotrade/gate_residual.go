package autotrade

import (
	"context"
	"fmt"
	"strings"
)

// gate_residual.go —— D-015 ①：残腿闸。开仓门禁新增前置检查——
// 任一持仓处于 CLOSING∪LEGGED（残腿裸露窗口）或任一 venue 平仓通道降级态
// （reconciler fastRetry 升级置位、成功清零）→ 拒新开 fail-closed。
// 事故锚：#505 gate 腿裸露 2h23m、两 venue「平仓通道降级」告警中，14:58 仍放行 #506。

// SetDegradedVenueFn 注入 reconciler 降级态读口（装配层 recon.DegradedVenues；
// nil=未接线只查残腿半闸——不猜通道健康）。
func (g *Gate) SetDegradedVenueFn(fn func() []string) { g.degradedVenueFn = fn }

// residualLegGate 残腿/降级拒开。fail-closed：查询失败=拒绝（与检查 2-4 同风格）。
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
	if g.degradedVenueFn != nil {
		if dv := g.degradedVenueFn(); len(dv) > 0 {
			reason := fmt.Sprintf("venue close-channel degraded: %s", strings.Join(dv, ","))
			g.rejectLog(ctx, *intent, reason, false)
			return GateResult{Status: "REJECTED", Reason: reason}, true
		}
	}
	return GateResult{}, false
}
