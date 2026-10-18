package autotrade

import (
	"context"
	"testing"
	"time"

	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// grantLiveCP D-620：测试用已授权 LIVE 控制面（live_gates active 行+mode=LIVE+readiness 真）。
// 聚焦非闸逻辑的测试用此件替代旧 LiveTrade=true 语义（真金腿过单闸）。
func grantLiveCP(t *testing.T, s *pgstore.Store) *gate.ControlPlane {
	t.Helper()
	cp := gate.NewControlPlane(s)
	cp.SetLiveGateChecker(gate.NewLiveGateChecker(s, func(context.Context) bool { return true }, "live"))
	if err := cp.GrantLive(context.Background(), "test", "t", "test", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("grant live: %v", err)
	}
	return cp
}
