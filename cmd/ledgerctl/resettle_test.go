// main_test.go —— D-422 S1：resettle 工具测试（-pos/-all/DSN 安全闸/dry-run/history unreachable 保留）。
package main

import (
	"context"
	"testing"

	"arb/internal/ledger/reconcile"
	"arb/internal/platform/testutil"
	"arb/internal/store/pgstore"
)

func TestD422_DBNameFromDSN(t *testing.T) {
	cases := []struct {
		dsn  string
		want string
	}{
		{"postgres://arb:arb@host:5432/arb?sslmode=disable", "arb"},
		{"postgres://arb:arb@host:5432/arb_test?sslmode=disable", "arb_test"},
		{"postgres://arb:arb@arb-postgres:5432/arb_exec_test", "arb_exec_test"},
	}
	for _, c := range cases {
		if got := dbNameFromDSN(c.dsn); got != c.want {
			t.Errorf("dbNameFromDSN(%q)=%q want %q", c.dsn, got, c.want)
		}
	}
}

// 安全闸：arb 库名拒绝（"arb" 出现在用户名不算）；_test 放行；allow-live 放行。
func TestD422_DSNSafety(t *testing.T) {
	if err := checkDSNSafety("postgres://arb:arb@host:5432/arb?sslmode=disable", false); err == nil {
		t.Error("arb 库名无 -allow-live 应拒绝")
	}
	if err := checkDSNSafety("postgres://arb:arb@host:5432/arb_exec_test?sslmode=disable", false); err != nil {
		t.Errorf("_test 库应放行: %v", err)
	}
	if err := checkDSNSafety("postgres://arb:arb@host:5432/arb?sslmode=disable", true); err != nil {
		t.Errorf("-allow-live 应放行: %v", err)
	}
}

func seedResettlePos(t *testing.T, s *pgstore.Store, oppID, note string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps, realized_pnl, note, opened_at, closed_at)
		 VALUES ($1,'XASSET_CARRY','CLOSED',10,-1.42,$2, now()-interval '2 days', now()) RETURNING id`, oppID, note).Scan(&id); err != nil {
		t.Fatalf("seed pos: %v", err)
	}
	return id
}

// -pos dry-run：纯 CEX 腿仓（无可校准腿=公式价权威）→ would resettle（不写库）。
func TestD422_ResettleDryRunPureCEX(t *testing.T) {
	s := testStoreResettle(t)
	ctx := context.Background()
	posID := seedResettlePos(t, s, "XASSET:XAUUSD:x", "pnl_estimated: partial or no calibration")
	openPx, closePx := 100.0, 110.0
	for _, leg := range []pgstore.PositionLeg{
		{PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: "okx:perp", Symbol: "X", Side: "SELL",
			Qty: 2, OrderID: "o1", Status: "FILLED", PriceActual: &openPx},
		{PositionID: posID, LegNo: 0, Phase: "CLOSE", Venue: "okx:perp", Symbol: "X", Side: "BUY",
			Qty: 2, OrderID: "c1", Status: "FILLED", PriceActual: &closePx},
	} {
		l := leg
		if err := s.CreatePositionLeg(ctx, &l); err != nil {
			t.Fatalf("seed leg: %v", err)
		}
	}
	tool := &resettler{store: s, deps: &settleDeps{store: s}, queriers: map[string]reconcile.OrderHistoryQuerier{}, qerr: map[string]error{}}
	tool.run(ctx, posID, true) // dry：只报告
	// note 未被清（dry-run 不写库）
	var note string
	if err := s.Pool().QueryRow(ctx, `SELECT COALESCE(note,'') FROM positions WHERE id=$1`, posID).Scan(&note); err != nil {
		t.Fatalf("note: %v", err)
	}
	if note == "" {
		t.Fatal("dry-run 不应清 note")
	}
}

// -pos 实跑：纯 CEX 仓 → 清 note + realized_pnl 重写 + skipped→pending + SettleAt 产 settlement。
func TestD422_ResettleRealRunPureCEX(t *testing.T) {
	s := testStoreResettle(t)
	ctx := context.Background()
	posID := seedResettlePos(t, s, "XASSET:XAUUSD:y", "pnl_estimated: partial or no calibration")
	openPx, closePx := 100.0, 110.0
	for _, leg := range []pgstore.PositionLeg{
		{PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: "okx:perp", Symbol: "X", Side: "SELL",
			Qty: 2, OrderID: "o1", Status: "FILLED", PriceActual: &openPx},
		{PositionID: posID, LegNo: 0, Phase: "CLOSE", Venue: "okx:perp", Symbol: "X", Side: "BUY",
			Qty: 2, OrderID: "c1", Status: "FILLED", PriceActual: &closePx},
	} {
		l := leg
		if err := s.CreatePositionLeg(ctx, &l); err != nil {
			t.Fatalf("seed leg: %v", err)
		}
	}
	tool := &resettler{store: s, deps: &settleDeps{store: s}, queriers: map[string]reconcile.OrderHistoryQuerier{}, qerr: map[string]error{}}
	tool.run(ctx, posID, false)
	var note string
	var pnl float64
	if err := s.Pool().QueryRow(ctx, `SELECT COALESCE(note,''), realized_pnl FROM positions WHERE id=$1`, posID).
		Scan(&note, &pnl); err != nil {
		t.Fatalf("pos read: %v", err)
	}
	if note != "" {
		t.Fatalf("note 应被清（纯 CEX 无可校准腿），got %q", note)
	}
	// CEX 公式价：(110−100)×(−1)×2 = −20
	if pnl != -20.0 {
		t.Fatalf("realized_pnl=%v want −20（CEX 公式价）", pnl)
	}
	// settlement 行产出（SettleAt 已跑，note 清除后不再 skipped-by-note）
	var completeness string
	if err := s.Pool().QueryRow(ctx,
		`SELECT COALESCE(completeness_status,'') FROM position_settlements WHERE position_id=$1 ORDER BY settled_at DESC LIMIT 1`,
		posID).Scan(&completeness); err != nil {
		t.Fatalf("settlement read: %v", err)
	}
	if completeness == "" || completeness == "skipped" {
		t.Fatalf("completeness=%q want 非 skipped（note 清除后应走完整结算）", completeness)
	}
}

// MT 腿但无 broker account → history unreachable, keep（note/统计态不动）。
func TestD422_ResettleHistoryUnreachableKeep(t *testing.T) {
	s := testStoreResettle(t)
	ctx := context.Background()
	posID := seedResettlePos(t, s, "XASSET:XAUUSD:z", "pnl_estimated: partial or no calibration")
	openPx, closePx := 100.0, 110.0
	for _, leg := range []pgstore.PositionLeg{
		{PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: "ghost:mt5", Symbol: "XAUUSD", Side: "BUY",
			Qty: 1, OrderID: "T1", Status: "FILLED", PriceActual: &openPx},
		{PositionID: posID, LegNo: 0, Phase: "CLOSE", Venue: "ghost:mt5", Symbol: "XAUUSD", Side: "SELL",
			Qty: 1, OrderID: "T2", Status: "FILLED", PriceActual: &closePx},
	} {
		l := leg
		if err := s.CreatePositionLeg(ctx, &l); err != nil {
			t.Fatalf("seed leg: %v", err)
		}
	}
	tool := &resettler{store: s, deps: &settleDeps{store: s}, queriers: map[string]reconcile.OrderHistoryQuerier{}, qerr: map[string]error{}}
	tool.run(ctx, posID, false)
	var note string
	if err := s.Pool().QueryRow(ctx, `SELECT COALESCE(note,'') FROM positions WHERE id=$1`, posID).Scan(&note); err != nil {
		t.Fatalf("note: %v", err)
	}
	if note == "" {
		t.Fatal("history unreachable 应保留 pnl_estimated note（不可得≠成功）")
	}
}

func testStoreResettle(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_EXEC_TEST_PG_DSN")
	s, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := s.Pool().Exec(context.Background(),
		`TRUNCATE positions, position_legs, position_cashflows, position_settlements, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}
