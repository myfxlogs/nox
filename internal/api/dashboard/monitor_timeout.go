// monitor_timeout.go —— D-575 事件热路径无界 RPC→per-call 超时闸常量与 helper。
// （2026-09-29 生产 25min 冻结止血：mtapi unary 面挂死→AfterFunc flush 内
// ServerTimezone 无界 RPC 冻结整条发射链；一次性 timer 冻结期间 dirty 无限积压零事件）。
package dashboard

import (
	"context"
	"time"
)

// monitorCallTimeout=10s 单调用界（对齐 margin/swap 既有量级，给网关慢回合法窗口）；
// bpFlushBound=20s flush 整体界（内含 tz RPC+annotate DB+facts DB 多次串行调用取裕量）。
// 超界=诚实失败走既有 err 路径（tz err→loc=nil proto 照发；querier err→offline），零语义变更。
var monitorCallTimeout = 10 * time.Second
var bpFlushBound = 20 * time.Second

// monitorCallCtx D-575：事件路径外部调用的 per-call 派生 ctx（调用后立即 cancel，
// 循环内禁 defer 累积）。
func monitorCallCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, monitorCallTimeout)
}

// flushBoundCtx D-575：flush 调用方统一 20s 界（Refresh/poll/kick 直调全覆盖——
// AfterFunc Background 在 dirtyLocked 内同款捕获 bound 值）。
func flushBoundCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, bpFlushBound)
}

// flushBounded D-575 F-3：两个 monitor 直调 flush 的统一直接闸（消最后无界调用方）。
func (m *BrokerPositionMonitor) flushBounded(ctx context.Context) {
	fctx, cancel := flushBoundCtx(ctx)
	defer cancel()
	m.flush(fctx)
}
func (m *AccountMonitor) flushBounded(ctx context.Context) {
	fctx, cancel := flushBoundCtx(ctx)
	defer cancel()
	m.flush(fctx)
}
