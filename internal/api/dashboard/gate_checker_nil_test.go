package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// grantedLiveChecker D-620 测试基件：已授权 LIVE 控制面（live_gates active+mode=LIVE）。
func grantedLiveChecker(t *testing.T, st *pgstore.Store) *gate.ControlPlane {
	t.Helper()
	cp := gate.NewControlPlane(st)
	cp.SetLiveGateChecker(gate.NewLiveGateChecker(st, func(context.Context) bool { return true }, "live"))
	if err := cp.GrantLive(context.Background(), "test", "t", "test", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("grant live: %v", err)
	}
	return cp
}

// TestGateCheckerNil_FailClosed D-620 核验补正Ⓑ：gateChecker nil 全径 fail-closed——
// 三执行径 FAILED「控制面未接线」+ 展示径 KillSwitch=true（宁显停不显通）。
func TestGateCheckerNil_FailClosed(t *testing.T) {
	// 执行径：pipeline 就位而 gateChecker nil → FAILED「控制面未接线」（先撞控制面再谈下游）。
	svc := &Service{pipeline: &exec.Pipeline{}}
	ctx := context.Background()
	if st, msg, _ := svc.ExecuteOpportunityInternal(ctx, "x", "r", 1); st != "FAILED" || !strings.Contains(msg, "控制面未接线") {
		t.Errorf("execute_internal nil: got %s %q, want FAILED 控制面未接线", st, msg)
	}
	if st, msg, _ := svc.ExecuteMeanRevert(ctx, "X", "a:mt5", "b:mt5", "r", 1, 0); st != "FAILED" || !strings.Contains(msg, "控制面未接线") {
		t.Errorf("meanrevert nil: got %s %q, want FAILED 控制面未接线", st, msg)
	}
	// 展示径：nil → KillSwitch=true（宁显停不显通）。
	ov := &Service{store: testServiceStore(t)}
	cresp, err := ov.GetOverviewBundle(ctx, nil)
	if err != nil {
		t.Fatalf("overview bundle: %v", err)
	}
	if !cresp.Msg.KillSwitch {
		t.Error("overview nil gateChecker: KillSwitch want true（展示 fail-closed）")
	}
}
