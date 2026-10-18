// wire_livegate.go —— D-620⑦：真金单闸装配件（main() ≤300 行棘轮门禁拆出）。
package main

import (
	"context"

	"nox/internal/data/bus"
	"nox/internal/platform/config"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// wireLiveGateChain 构造 lg+cp 单实例——liveGate readiness 接 readyz Tradable
// （liveGate 参数 nil 防自依环）、environment 构造注入（空串=Check 永拒 fail-closed）。
// cp 下穿 dashboard 执行径/dciexec 提案执行器/selfwatch——统一实例非各自建件。
func wireExecGateChain(st *pgstore.Store, qbus *bus.QuoteBus, cfg *config.Config) (*exec.Registry, *gate.LiveGateChecker, *gate.ControlPlane) {
	execReg := exec.NewRegistry()
	lg := gate.NewLiveGateChecker(st, func(rctx context.Context) bool {
		return readyzCheck(rctx, st, qbus, cfg, execReg, nil).Tradable
	}, cfg.Environment)
	cp := gate.NewControlPlane(st)
	cp.SetLiveGateChecker(lg)
	return execReg, lg, cp
}
