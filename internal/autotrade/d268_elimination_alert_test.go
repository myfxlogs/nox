// D-268 S3 单测：策略淘汰告警 runStartupElimination（boot 时 → AlertSink）。
// 参照 d256a_revival_alert_test.go AlertSink spy 模式——用 AlertSink spy 捕获告警。
// 需要 test-pg2 DSN（ARB_TEST_PG_DSN）——DB 设置仓位/结算数据，AlertSink spy 验证告警。
package autotrade

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"arb/internal/store/pgstore"
)

// insertElimFixture 插入 7 笔 PERP_FUNDING CLOSED 仓位 + 对应 position_settlements。
// realized 列表决定每笔净值（正/负），用于触发/不触发淘汰。
func insertElimFixture(t *testing.T, s *pgstore.Store, realized []float64) {
	t.Helper()
	ctx := context.Background()
	for i, r := range realized {
		var posID int64
		if err := s.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
			 VALUES ($1, 'PERP_FUNDING', 'CLOSED', now(), now(), $2) RETURNING id`,
			"PFUND:d268:vA:vB:"+strconv.Itoa(i), r).Scan(&posID); err != nil {
			t.Fatalf("insert pos %d: %v", i, err)
		}
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd)
			 VALUES ($1, 'PERP_FUNDING:vA:vB', 10.0, $2, 1.0, now(), $2)`,
			posID, r); err != nil {
			t.Fatalf("insert settlement pos %d: %v", i, err)
		}
	}
}

// TestD268_S3_EliminationAlerts 7 笔负净值 → 淘汰 → AlertSink warn [策略淘汰]。
func TestD268_S3_EliminationAlerts(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE positions, position_legs, strategy_stats, position_settlements, autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 7 笔负净值：median=-1, avg=-1.57 → 小样本双负触发淘汰
	insertElimFixture(t, s, []float64{-3, -3, -2, -1, -1, -0.5, -0.5})

	e := &Engine{store: s}
	var mu sync.Mutex
	var alerts []string
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}

	e.runStartupElimination(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 1 {
		t.Fatalf("elimination: want 1 alert, got %d: %v", len(alerts), alerts)
	}
	if !strings.HasPrefix(alerts[0], "warn:") {
		t.Errorf("level: want warn, got %s", alerts[0])
	}
	if !strings.Contains(alerts[0], "[策略淘汰]") {
		t.Errorf("msg: want [策略淘汰], got %s", alerts[0])
	}
}

// TestD268_S3_NoEliminationNoAlert 7 笔正净值 → 不淘汰 → AlertSink 0 次。
func TestD268_S3_NoEliminationNoAlert(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE positions, position_legs, strategy_stats, position_settlements, autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 7 笔正净值：median>0, avg>0 → 不触发淘汰
	insertElimFixture(t, s, []float64{3, 3, 2, 1, 1, 0.5, 0.5})

	e := &Engine{store: s}
	var mu sync.Mutex
	var alerts []string
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}

	e.runStartupElimination(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 0 {
		t.Fatalf("no elimination: want 0 alerts, got %d: %v", len(alerts), alerts)
	}
}
