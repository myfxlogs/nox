// gate_live_test.go —— D-620 T2：真金单闸模式判据版（Gate.Submit 路径：mode!=LIVE 拒实盘腿）。
package gate

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// setupLiveGateStore 构造真库 store：autotrade_state=RUNNING + broker_accounts 种子 is_real。
func setupLiveGateStore(t *testing.T, venueLong, venueShort string, longReal, shortReal bool) *pgstore.Store {
	t.Helper()
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats, fin_strategy, autotrade_logs, positions, position_legs, position_cashflows, broker_accounts, execution_controls, execution_control_events, live_gates CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert autotrade_state: %v", err)
	}
	insert := func(venue string, isReal bool) {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real)
			 VALUES ($1, 'mt5', 'h', 443, 'p', 's', 'connected', $2) ON CONFLICT DO NOTHING`,
			venue, isReal); err != nil {
			t.Fatalf("insert broker_account %s: %v", venue, err)
		}
	}
	vL, _, _ := strings.Cut(venueLong, ":")
	vS, _, _ := strings.Cut(venueShort, ":")
	insert(vL, longReal)
	insert(vS, shortReal)
	return s
}

// paperCP 构造 PAPER 模式控制面（execution_controls PAPER/enabled 行）。
func paperCP(t *testing.T, s *pgstore.Store) *ControlPlane {
	t.Helper()
	cp := NewControlPlane(s)
	if err := cp.SetMode(context.Background(), ModePaper, "t", "t"); err != nil {
		t.Fatalf("set mode paper: %v", err)
	}
	return cp
}

// grantedLiveCP 构造已授权 LIVE 控制面（live_gates active 行+mode=LIVE+readiness 真）。
func grantedLiveCP(t *testing.T, s *pgstore.Store) *ControlPlane {
	t.Helper()
	cp := NewControlPlane(s)
	cp.SetLiveGateChecker(NewLiveGateChecker(s, func(context.Context) bool { return true }, "live"))
	if err := cp.GrantLive(context.Background(), "t", "t", "T2", time.Now().Add(time.Hour), TradableReport{Tradable: true}); err != nil {
		t.Fatalf("grant live: %v", err)
	}
	return cp
}

// TestGateLive_PaperMode_RealVenue_Rejected D-620 T2①：mode=PAPER + is_real=true 腿 → REJECTED 含「非 LIVE」。
func TestGateLive_PaperMode_RealVenue_Rejected(t *testing.T) {
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", true, false)
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5}, paperCP(t, s))
	intent := TradeIntent{
		Strategy: "CROSS_VENUE", Canonical: "AUDJPY", VenueBuy: "vA:mt5", VenueSell: "vB:mt5",
		Qty: 0.01, DedupKey: "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			t.Error("Execute should not be called (非 LIVE 拒)")
			return "FAILED", "should not reach", 0
		},
	}
	res := g.Submit(context.Background(), &intent)
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "非 LIVE") {
		t.Errorf("PAPER+real: got %s reason=%q, want REJECTED 含 非 LIVE", res.Status, res.Reason)
	}
}

// TestGateLive_GrantedLive_RealVenue_PassGate D-620 T2②：GrantLive 后 is_real=true → 过真金闸（至下游检查）。
func TestGateLive_GrantedLive_RealVenue_PassGate(t *testing.T) {
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", true, false)
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5, Environment: ""}, grantedLiveCP(t, s))
	intent := TradeIntent{
		Strategy: "CROSS_VENUE", Canonical: "AUDJPY", VenueBuy: "vA:mt5", VenueSell: "vB:mt5",
		Qty: 0.01, DedupKey: "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) { return "EXECUTED", "", 1 },
	}
	res := g.Submit(context.Background(), &intent)
	if res.Status == "REJECTED" && strings.Contains(res.Reason, "非 LIVE") {
		t.Errorf("granted LIVE should pass 真金闸, rejected: %s", res.Reason)
	}
}

// TestGateLive_PaperMode_DemoVenue_PassGate D-620 T2③：mode=PAPER + demo 腿 → 过真金闸。
func TestGateLive_PaperMode_DemoVenue_PassGate(t *testing.T) {
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", false, false)
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5, Environment: ""}, paperCP(t, s))
	intent := TradeIntent{
		Strategy: "CROSS_VENUE", Canonical: "AUDJPY", VenueBuy: "vA:mt5", VenueSell: "vB:mt5",
		Qty: 0.01, DedupKey: "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) { return "EXECUTED", "", 1 },
	}
	res := g.Submit(context.Background(), &intent)
	if res.Status == "REJECTED" && strings.Contains(res.Reason, "非 LIVE") {
		t.Errorf("PAPER+demo should pass 真金闸, rejected: %s", res.Reason)
	}
}
