// gate_live_test.go —— D-159 W6a S2：LIVE 人工开关单测（Gate.Submit 路径）。
package autotrade

import (
	"context"
	"strings"
	"testing"

	"arb/internal/store/pgstore"
)

// setupLiveGateStore 构造真库 store：autotrade_state=RUNNING + broker_accounts 种子 is_real。
func setupLiveGateStore(t *testing.T, venueLong, venueShort string, longReal, shortReal bool) *pgstore.Store {
	t.Helper()
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats, autotrade_logs, positions, position_legs, position_cashflows, broker_accounts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert autotrade_state: %v", err)
	}
	insert := func(venue string, isReal bool) {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO broker_accounts (venue, platform, host, port, user_id, password, server, status, is_real)
			 VALUES ($1, 1, 'h', 443, 1, 'p', 's', 'connected', $2) ON CONFLICT DO NOTHING`,
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

// TestGateLive_RealVenue_LiveTradeFalse_Rejected D-159 W6a S2：is_real=true + LiveTrade=false → REJECTED。
func TestGateLive_RealVenue_LiveTradeFalse_Rejected(t *testing.T) {
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", true, false)
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: false})
	intent := TradeIntent{
		Strategy:  "CROSS_VENUE",
		Canonical: "AUDJPY",
		VenueBuy:  "vA:mt5",
		VenueSell: "vB:mt5",
		Qty:       0.01,
		DedupKey:  "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			t.Error("Execute should not be called (LIVE gate reject)")
			return "FAILED", "should not reach", 0
		},
	}
	res := g.Submit(context.Background(), &intent)
	if res.Status != "REJECTED" {
		t.Errorf("is_real=true + LiveTrade=false: got %s, want REJECTED (reason=%s)", res.Status, res.Reason)
	}
}

// TestGateLive_RealVenue_LiveTradeTrue_PassGate D-159 W6a S2：is_real=true + LiveTrade=true → 通过 LIVE gate（到下一检查）。
// 注：可能被其他 gate 拒绝（如 combo allow），但不应是 LIVE gate 拒绝。
func TestGateLive_RealVenue_LiveTradeTrue_PassGate(t *testing.T) {
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", true, false)
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, Environment: ""}) // Environment 空=不检查 environment
	intent := TradeIntent{
		Strategy:  "CROSS_VENUE",
		Canonical: "AUDJPY",
		VenueBuy:  "vA:mt5",
		VenueSell: "vB:mt5",
		Qty:       0.01,
		DedupKey:  "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			return "EXECUTED", "", 1
		},
	}
	res := g.Submit(context.Background(), &intent)
	// 不应因 LIVE gate 拒绝（LiveTrade=true 放行实盘）
	if res.Status == "REJECTED" && strings.Contains(res.Reason, "live trade disabled") {
		t.Errorf("LiveTrade=true should pass LIVE gate, but rejected: %s", res.Reason)
	}
}

// TestGateLive_DemoVenue_LiveTradeFalse_PassGate D-159 W6a S2：is_real=false（demo）+ LiveTrade=false → 放行（demo 不受 LIVE gate 限制）。
func TestGateLive_DemoVenue_LiveTradeFalse_PassGate(t *testing.T) {
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", false, false)
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: false, Environment: ""})
	intent := TradeIntent{
		Strategy:  "CROSS_VENUE",
		Canonical: "AUDJPY",
		VenueBuy:  "vA:mt5",
		VenueSell: "vB:mt5",
		Qty:       0.01,
		DedupKey:  "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			return "EXECUTED", "", 1
		},
	}
	res := g.Submit(context.Background(), &intent)
	if res.Status == "REJECTED" && strings.Contains(res.Reason, "live trade disabled") {
		t.Errorf("demo venue should not be blocked by LIVE gate, but rejected: %s", res.Reason)
	}
}
