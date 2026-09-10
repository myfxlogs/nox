// D-268 S1 单测：autotrade_state 状态转换告警（stateMachineTick → AlertSink）。
// 参照 d256a_revival_alert_test.go AlertSink spy 模式——用 AlertSink spy 捕获告警。
// 需要 test-pg2 DSN（ARB_TEST_PG_DSN）——DB 设置状态/日亏，AlertSink spy 验证告警。
package autotrade

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// setupStateTestStore 构造带 DB store 的 Engine（清表+设状态），供 stateMachineTick 测试。
func setupStateTestStore(t *testing.T, state string, haltedOn *time.Time) *Engine {
	t.Helper()
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE positions, position_legs, autotrade_state, strategy_stats, position_settlements CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.SetAutotradeState(ctx, state, "test init", haltedOn); err != nil {
		t.Fatalf("set autotrade state %s: %v", state, err)
	}
	return &Engine{
		store:         s,
		cfg:           Config{MaxDailyLoss: 100},
		firstTickDone: true, // 跳过 runStartupElimination（S1 只测状态转换）
	}
}

// TestD268_S1_DegradeToCritical RUNNING + dailyPnl < -MaxDailyLoss → DAILY_LOSS_HALT → AlertSink critical [状态降级]。
func TestD268_S1_DegradeToCritical(t *testing.T) {
	e := setupStateTestStore(t, StateRunning, nil)
	ctx := context.Background()

	// 插入一笔 CLOSED 仓位 realized_pnl=-200 → dailyPnl=-200 < -MaxDailyLoss(100)
	if _, err := e.store.Pool().Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
		 VALUES ('D268:degrade:vA:vB', 'CROSS_VENUE', 'CLOSED', now(), now(), -200)`); err != nil {
		t.Fatalf("insert position: %v", err)
	}

	var mu sync.Mutex
	var alerts []string
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}

	e.stateMachineTick(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 1 {
		t.Fatalf("degrade: want 1 alert, got %d: %v", len(alerts), alerts)
	}
	if !strings.HasPrefix(alerts[0], "critical:") {
		t.Errorf("level: want critical, got %s", alerts[0])
	}
	if !strings.Contains(alerts[0], "[状态降级]") {
		t.Errorf("msg: want [状态降级], got %s", alerts[0])
	}
	if !strings.Contains(alerts[0], StateRunning) || !strings.Contains(alerts[0], StateDailyLossHalt) {
		t.Errorf("msg: want %s→%s, got %s", StateRunning, StateDailyLossHalt, alerts[0])
	}
}

// TestD268_S1_RecoverToWarn DAILY_LOSS_HALT + haltedOn > 24h → RUNNING → AlertSink warn [状态恢复]。
func TestD268_S1_RecoverToWarn(t *testing.T) {
	haltedOn := time.Now().UTC().Truncate(24 * time.Hour).Add(-48 * time.Hour) // 2 天前
	e := setupStateTestStore(t, StateDailyLossHalt, &haltedOn)
	ctx := context.Background()

	var mu sync.Mutex
	var alerts []string
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}

	e.stateMachineTick(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 1 {
		t.Fatalf("recover: want 1 alert, got %d: %v", len(alerts), alerts)
	}
	if !strings.HasPrefix(alerts[0], "warn:") {
		t.Errorf("level: want warn, got %s", alerts[0])
	}
	if !strings.Contains(alerts[0], "[状态恢复]") {
		t.Errorf("msg: want [状态恢复], got %s", alerts[0])
	}
	if !strings.Contains(alerts[0], StateDailyLossHalt) || !strings.Contains(alerts[0], StateRunning) {
		t.Errorf("msg: want %s→%s, got %s", StateDailyLossHalt, StateRunning, alerts[0])
	}
}

// TestD268_S1_NilAlertSinkNoPanic AlertSink nil → 状态转换发生但不 panic。
func TestD268_S1_NilAlertSinkNoPanic(t *testing.T) {
	e := setupStateTestStore(t, StateRunning, nil)
	ctx := context.Background()

	if _, err := e.store.Pool().Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
		 VALUES ('D268:nil:vA:vB', 'CROSS_VENUE', 'CLOSED', now(), now(), -200)`); err != nil {
		t.Fatalf("insert position: %v", err)
	}

	// AlertSink nil — 不应 panic
	e.stateMachineTick(ctx)

	// 验证状态确实转换了（SetAutotradeState 成功）
	stateRow, err := e.store.GetAutotradeState(ctx)
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if stateRow.State != StateDailyLossHalt {
		t.Errorf("state: want %s, got %s", StateDailyLossHalt, stateRow.State)
	}
}
