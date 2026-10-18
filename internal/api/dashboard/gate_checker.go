package dashboard

import (
	"context"
)

// GateChecker D-195-A：执行控制面检查接口（D-620⑥：实现唯一=*gate.ControlPlane，
// storeGateChecker 复制件已退役——dashboard 不再自建旁路控制面）。
type GateChecker interface {
	CheckGate(ctx context.Context) (mode string, allowed bool, reason string, err error)
	IsKillSwitchActive(ctx context.Context) (bool, error)
}
