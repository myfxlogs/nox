// service_swap_deviation_test.go —— D-182 S4：手动 RPC swap 偏差拦截单测。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// setupSwapDeviationServiceStore 构造真库 store + broker_accounts + facts + position+legs+snapshots。
// factsLongBps/factsShortBps：swap_long/swap_short facts 规格值（bps）。
// actualUSD：broker 实际日增量（USD，写入 position_swap_snapshots）。
// posQty/posPrice：持仓腿 qty/price（notional=qty×price×CS，CS=1 简化）。
func setupSwapDeviationServiceStore(t *testing.T, venueLong, venueShort string, factsLongBps, factsShortBps, actualUSD, posQty, posPrice float64) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	s, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	// 清表（顺序：依赖关系）
	for _, tbl := range []string{"position_swap_snapshots", "position_legs", "positions", "facts", "broker_accounts", "autotrade_state"} {
		if _, err := s.Pool().Exec(ctx, "TRUNCATE "+tbl+" CASCADE"); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	// autotrade_state RUNNING（避免其他检查干扰，本测试只测 swap deviation）
	if _, err := s.Pool().Exec(ctx, `INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert autotrade_state: %v", err)
	}
	// broker_accounts
	vL, _, _ := strings.Cut(venueLong, ":")
	vS, _, _ := strings.Cut(venueShort, ":")
	for _, v := range []string{vL, vS} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO broker_accounts (venue, platform, host, port, user_id, password, server, status, is_real)
			 VALUES ($1, 1, 'h', 443, 1, 'p', 's', 'connected', false) ON CONFLICT DO NOTHING`, v); err != nil {
			t.Fatalf("insert broker_account %s: %v", v, err)
		}
	}
	// facts
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: vL, Symbol: "AUDJPY", Value: factsLongBps, Unit: fact.UnitBps, Ts: now, Src: "test"},
		{Kind: fact.KindSwapShort, Venue: vS, Symbol: "AUDJPY", Value: factsShortBps, Unit: fact.UnitBps, Ts: now, Src: "test"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	// position + legs（OPEN，匹配 venueLong）
	var posID int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ('SWAPARB:AUDJPY:vA:vB', 'SWAPARB', 'OPEN', NOW()) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	price := posPrice
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, account, symbol, side, qty, price_expected, price_actual, status)
		 VALUES ($1, 1, 'OPEN', $2, 'acct', 'AUDJPY', 'BUY', $3, $4, $4, 'FILLED')`,
		posID, venueLong, posQty, price); err != nil {
		t.Fatalf("insert position_leg: %v", err)
	}
	// swap snapshots：昨日末条 swap_usd=0，今日末条 swap_usd=actualUSD → DailySwapAccrued=actualUSD
	// venue 用 bare（vA），与 DailySwapAccrued 查询参数一致（gate_checks.go 同模式）。
	yesterday := time.Now().UTC().Add(-24 * time.Hour)
	today := time.Now().UTC()
	if err := s.InsertSwapSnapshot(ctx, posID, vL, 0, yesterday); err != nil {
		t.Fatalf("insert swap snapshot yesterday: %v", err)
	}
	if err := s.InsertSwapSnapshot(ctx, posID, vL, actualUSD, today); err != nil {
		t.Fatalf("insert swap snapshot today: %v", err)
	}
	return s
}

// TestSwapDeviationBetweenPct 纯函数测试。
func TestSwapDeviationBetweenPct(t *testing.T) {
	tests := []struct {
		name   string
		facts  float64
		actual float64
		want   float64
	}{
		{"both zero → 0", 0, 0, 0},
		{"facts=0 actual≠0 → 100", 0, 1.0, 100},
		{"equal → 0", 5.0, 5.0, 0},
		{"50% deviation", 10.0, 5.0, 50},
		{"100% deviation (2x)", 10.0, 20.0, 50}, // |10-20|/max(10,20)=10/20=50%
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := swapDeviationBetweenPct(tt.facts, tt.actual)
			if absDiff(got, tt.want) > 0.01 {
				t.Errorf("swapDeviationBetweenPct(%.2f, %.2f) = %.2f, want %.2f", tt.facts, tt.actual, got, tt.want)
			}
		})
	}
}

// TestCheckSwapDeviationGate_HighDeviation_Rejected D-182 S4：偏差 > 50% → 拒绝。
// facts=0 bps（factsUSD=0），actual=-2.21 USD → 偏差 100% > 50% → 拒绝。
func TestCheckSwapDeviationGate_HighDeviation_Rejected(t *testing.T) {
	s := setupSwapDeviationServiceStore(t, "vA:mt5", "vB:mt5", 0.0, 0.0, -2.21, 0.01, 100.0)
	svc := &Service{store: s, swapDeviationGatePct: 50.0}
	msg := svc.checkSwapDeviationGate(context.Background(), "AUDJPY", "vA:mt5", "vB:mt5")
	if msg == "" {
		t.Fatal("high deviation: want non-empty reject reason, got empty")
	}
	if !strings.Contains(msg, "swap deviation gate") {
		t.Errorf("want reason contains 'swap deviation gate', got %q", msg)
	}
}

// TestCheckSwapDeviationGate_LowDeviation_Pass D-182 S4：偏差 < 50% → 放行。
// facts=0.5 bps（notional=0.01×100=1，factsUSD=0.005），actual=0.005 USD → 偏差 0% < 50% → 放行。
func TestCheckSwapDeviationGate_LowDeviation_Pass(t *testing.T) {
	s := setupSwapDeviationServiceStore(t, "vA:mt5", "vB:mt5", 0.5, 0.0, 0.005, 0.01, 100.0)
	svc := &Service{store: s, swapDeviationGatePct: 50.0}
	msg := svc.checkSwapDeviationGate(context.Background(), "AUDJPY", "vA:mt5", "vB:mt5")
	if msg != "" {
		t.Errorf("low deviation: want empty (pass), got %q", msg)
	}
}

// TestCheckSwapDeviationGate_Disabled_Pass D-182 S4 反证：阈值=0（禁用）→ 高偏差也放行。
func TestCheckSwapDeviationGate_Disabled_Pass(t *testing.T) {
	s := setupSwapDeviationServiceStore(t, "vA:mt5", "vB:mt5", 0.0, 0.0, -2.21, 0.01, 100.0)
	svc := &Service{store: s, swapDeviationGatePct: 0} // 禁用
	msg := svc.checkSwapDeviationGate(context.Background(), "AUDJPY", "vA:mt5", "vB:mt5")
	if msg != "" {
		t.Errorf("disabled: want empty (pass), got %q", msg)
	}
}

// TestCheckSwapDeviationGate_NoHistory_Pass D-182 S4：无 OPEN 持仓 → 放行（首次开仓走 S3）。
func TestCheckSwapDeviationGate_NoHistory_Pass(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	s, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	for _, tbl := range []string{"position_swap_snapshots", "position_legs", "positions", "facts", "broker_accounts", "autotrade_state"} {
		if _, err := s.Pool().Exec(ctx, "TRUNCATE "+tbl+" CASCADE"); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert autotrade_state: %v", err)
	}
	svc := &Service{store: s, swapDeviationGatePct: 50.0}
	msg := svc.checkSwapDeviationGate(context.Background(), "AUDJPY", "vA:mt5", "vB:mt5")
	if msg != "" {
		t.Errorf("no history: want empty (pass), got %q", msg)
	}
}
