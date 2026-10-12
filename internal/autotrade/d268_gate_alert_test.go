// D-268 S2 单测：机会通过门禁+执行成功告警（gate.Submit EXECUTED → g.store.InsertAlert）。
// 参照 d256a_revival_alert_test.go spy 模式——S2 告警走 g.store.InsertAlert（非 AlertSink），
// 用 alerts 表查询验证（DB 设置门禁状态，查 alerts 表验证告警落库）。
// 需要 test-pg2 DSN（ARB_TEST_PG_DSN）。
package autotrade

import (
	"context"
	"strings"
	"testing"

	"arb/internal/store/pgstore"
)

// setupGateAlertStore 构造真库 store：autotrade_state=RUNNING + broker_accounts demo（is_real=false）。
func setupGateAlertStore(t *testing.T) *pgstore.Store {
	t.Helper()
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE autotrade_state, strategy_stats, autotrade_logs, positions, position_legs,
		        position_cashflows, broker_accounts, alerts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert autotrade_state: %v", err)
	}
	// demo 账户（is_real=false）→ LiveTrade=false 也能通过 LIVE gate
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real)
		 VALUES ('vA', 'mt5', 'h', 443, 'p', 's', 'connected', false),
		        ('vB', 'mt5', 'h', 443, 'p', 's', 'connected', false)`); err != nil {
		t.Fatalf("insert broker_accounts: %v", err)
	}
	return s
}

// countAlertsByPrefix 查 alerts 表中 message 含 prefix 的行数。
func countAlertsByPrefix(t *testing.T, s *pgstore.Store, prefix string) int {
	t.Helper()
	var n int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT COUNT(*) FROM alerts WHERE message LIKE $1`, prefix+"%").Scan(&n); err != nil {
		t.Fatalf("count alerts %s: %v", prefix, err)
	}
	return n
}

// TestD268_S2_ExecutedAlerts 通过全部门禁 + Execute=EXECUTED → alerts 表有 warn [机会执行]。
func TestD268_S2_ExecutedAlerts(t *testing.T) {
	s := setupGateAlertStore(t)
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: false, Environment: ""}, nil)
	intent := TradeIntent{
		Strategy:  "CROSS_VENUE",
		Canonical: "AUDJPY",
		VenueBuy:  "vA:mt5",
		VenueSell: "vB:mt5",
		Qty:       0.01,
		NetBps:    15.3,
		DedupKey:  "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			return "EXECUTED", "", 42
		},
	}

	res := g.Submit(context.Background(), &intent)
	if res.Status != "EXECUTED" {
		t.Fatalf("status: want EXECUTED, got %s (reason=%s)", res.Status, res.Reason)
	}

	n := countAlertsByPrefix(t, s, "[机会执行]")
	if n != 1 {
		t.Fatalf("alerts: want 1 [机会执行], got %d", n)
	}

	// 验证告警内容
	var level, msg string
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT level, message FROM alerts WHERE message LIKE '[机会执行]%' LIMIT 1`).Scan(&level, &msg); err != nil {
		t.Fatalf("query alert: %v", err)
	}
	if level != "warn" {
		t.Errorf("level: want warn, got %s", level)
	}
	if !strings.Contains(msg, "CROSS_VENUE") {
		t.Errorf("msg: want strategy CROSS_VENUE, got %s", msg)
	}
	if !strings.Contains(msg, "AUDJPY") {
		t.Errorf("msg: want canonical AUDJPY, got %s", msg)
	}
	if !strings.Contains(msg, "持仓#42") {
		t.Errorf("msg: want posID=42, got %s", msg)
	}
}

// TestD268_S2_RejectedNoAlert 策略已禁用 → REJECTED → alerts 表无 [机会执行]。
func TestD268_S2_RejectedNoAlert(t *testing.T) {
	s := setupGateAlertStore(t)
	ctx := context.Background()
	// 禁用策略 → Submit 在检查 2 拒绝
	if err := s.DisableStrategy(ctx, "CROSS_VENUE", "test disabled"); err != nil {
		t.Fatalf("disable strategy: %v", err)
	}
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: false, Environment: ""}, nil)
	intent := TradeIntent{
		Strategy:  "CROSS_VENUE",
		Canonical: "AUDJPY",
		VenueBuy:  "vA:mt5",
		VenueSell: "vB:mt5",
		Qty:       0.01,
		NetBps:    15.3,
		DedupKey:  "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			t.Error("Execute should not be called (REJECTED)")
			return "FAILED", "should not reach", 0
		},
	}

	res := g.Submit(ctx, &intent)
	if res.Status != "REJECTED" {
		t.Fatalf("status: want REJECTED, got %s", res.Status)
	}

	n := countAlertsByPrefix(t, s, "[机会执行]")
	if n != 0 {
		t.Fatalf("rejected: want 0 [机会执行] alerts, got %d", n)
	}
}
