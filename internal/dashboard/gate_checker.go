package dashboard

import (
	"context"

	"arb/internal/store/pgstore"
)

// GateChecker D-195-A：执行控制面检查接口（避免 dashboard→autotrade 循环依赖）。
// autotrade.ControlPlane 实现此接口；dashboard 内部轻量实现直接查 execution_controls。
type GateChecker interface {
	CheckGate(ctx context.Context) (mode string, allowed bool, reason string, err error)
}

// storeGateChecker dashboard 内部轻量 GateChecker（直接查 execution_controls，不 import autotrade）。
// 逻辑等价于 autotrade.ControlPlane.CheckGate（D-195 §3.4）。
type storeGateChecker struct {
	store *pgstore.Store
}

func (c *storeGateChecker) CheckGate(ctx context.Context) (mode string, allowed bool, reason string, err error) {
	ctrl, qerr := c.store.GetExecutionControl(ctx, "global", "")
	if qerr != nil {
		return "READ_ONLY", false, "query failed: " + qerr.Error(), qerr
	}
	if ctrl == nil {
		return "READ_ONLY", false, "no execution control row (default READ_ONLY)", nil
	}
	mode = ctrl.Mode
	if !ctrl.Enabled {
		return mode, false, "kill switch active", nil
	}
	if mode == "READ_ONLY" {
		return mode, false, "mode is READ_ONLY", nil
	}
	if mode == "PAPER" || mode == "TEST" {
		return mode, true, "allowed", nil
	}
	return mode, false, "unknown mode: " + mode, nil
}
