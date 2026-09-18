// D-332 T2/T3：XASSET_CARRY 进 checkAllStrategiesDisabled 与 runStartupElimination 清单。
// 仿 d268_elimination_alert_test.go 引擎构造（testAccumStore DSN + settlements 造数路径）。
package autotrade

import (
	"context"
	"strconv"
	"testing"

	"arb/internal/store/pgstore"
)

// insertXAssetElimFixture 插入 n 笔 XASSET_CARRY CLOSED 仓位+settlements（净值由 realized 给出）。
func insertXAssetElimFixture(t *testing.T, s *pgstore.Store, realized []float64) {
	t.Helper()
	ctx := context.Background()
	for i, r := range realized {
		var posID int64
		if err := s.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
			 VALUES ($1, 'XASSET_CARRY', 'CLOSED', now(), now(), $2) RETURNING id`,
			"XASSET:d332:vA:vB:"+strconv.Itoa(i), r).Scan(&posID); err != nil {
			t.Fatalf("insert pos %d: %v", i, err)
		}
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd)
			 VALUES ($1, 'XASSET_CARRY:vA:vB', 10.0, $2, 1.0, now(), $2)`,
			posID, r); err != nil {
			t.Fatalf("insert settlement pos %d: %v", i, err)
		}
	}
}

// TestD332_CheckAllStrategiesDisabled_XAssetEnabled T2：6 经典型全禁 + XASSET_CARRY
// 缺省（无 strategy_stats 行=未禁，autotrade.go:184 语义）→ 不算「全禁」，返 false。
// 红=现码清单缺 XASSET_CARRY → 6 型全禁即返 true（引擎 halt，XASSET 在持仓失去管理）。
func TestD332_CheckAllStrategiesDisabled_XAssetEnabled(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE positions, position_legs, strategy_stats, position_settlements, autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for _, typ := range []string{"CROSS_VENUE", "MEANREVERT", "SWAPARB", "CARRY", "FUNDING_HEDGE", "PERP_FUNDING"} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO strategy_stats (strategy, disabled) VALUES ($1, true)`, typ); err != nil {
			t.Fatalf("seed disabled %s: %v", typ, err)
		}
	}
	// XASSET_CARRY 不插行（缺省=未禁）
	e := &Engine{store: s}
	if e.checkAllStrategiesDisabled(ctx) {
		t.Error("6 经典型全禁 + XASSET_CARRY 未禁：应返 false（红=现码清单缺型返 true）")
	}
}

// TestD332_StartupElimination_XAssetCarry T3：XASSET_CARRY 7 笔双负（median=-1 avg=-1.57，
// 过 evaluateElimination 小样本双负线）→ runStartupElimination 后 IsStrategyDisabled=true。
// 红=现码清单缺型 → XASSET 永不进淘汰扫描（亏损策略永生）。
func TestD332_StartupElimination_XAssetCarry(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE positions, position_legs, strategy_stats, position_settlements, autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	insertXAssetElimFixture(t, s, []float64{-3, -3, -2, -1, -1, -0.5, -0.5})
	e := &Engine{store: s}
	e.runStartupElimination(ctx)
	disabled, err := s.IsStrategyDisabled(ctx, "XASSET_CARRY")
	if err != nil {
		t.Fatalf("IsStrategyDisabled: %v", err)
	}
	if !disabled {
		t.Error("XASSET_CARRY 7 笔双负应被启动淘汰扫描禁用（红=现码清单缺型恒 false）")
	}
}
