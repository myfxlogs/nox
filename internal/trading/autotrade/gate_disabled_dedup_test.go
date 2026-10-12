// gate_disabled_dedup_test.go —— runStartupElimination 侧锚（gate 域禁用去重测试迁 internal/risk/gate，D-212 拆搬）。
package autotrade

import (
	"context"
	"strconv"
	"testing"

	"arb/internal/risk/gate"
)

// 策略名用真实 CHECK 约束允许值（PERP_FUNDING 判杀 / SWAPARB 不杀）。
func TestRunStartupElimination_AggregateInput(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats, autotrade_logs, positions, position_legs, position_cashflows CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// 场景①：PERP_FUNDING 策略，7 笔全负（median<0 avg<0）→ 判杀
	// nets = [-3,-3,-2,-1,-1,-0.5,-0.5] → median=-1, avg=-1.57
	// D-193-A 复审退回 R1：AggregateStrategyStats 改读 position_settlements.net_realized_usd，
	// fixture 需同步插 settlement 行（net_realized_usd=realized_pnl），与 autotrade_aggregate_test.go 的
	// insertAggPosition 适配方式一致。
	for i, realized := range []float64{-3, -3, -2, -1, -1, -0.5, -0.5} {
		var posID int64
		if err := s.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
			 VALUES ($1, 'PERP_FUNDING', 'CLOSED', now(), now(), $2) RETURNING id`,
			"PFUND:kill:vA:vB:"+strconv.Itoa(i), realized).Scan(&posID); err != nil {
			t.Fatalf("insert pos %d: %v", i, err)
		}
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd)
			 VALUES ($1, 'PERP_FUNDING:vA:vB', 10.0, $2, 1.0, now(), $2)`,
			posID, realized); err != nil {
			t.Fatalf("insert settlement pos %d: %v", i, err)
		}
	}
	// strategy_stats 不插累计列（验证不读累计列）——仅 disabled=false 默认
	// runStartupElimination 用 GetStrategyStat 查 disabled（无行→零值 disabled=false）+ AggregateStrategyStats 聚合

	e := &Engine{store: s}
	e.runStartupElimination(ctx)

	disabled, err := s.IsStrategyDisabled(ctx, "PERP_FUNDING")
	if err != nil {
		t.Fatalf("IsStrategyDisabled PERP_FUNDING: %v", err)
	}
	if !disabled {
		t.Fatal("PERP_FUNDING 7 笔 median<0 avg<0 → runStartupElimination 应判杀禁用")
	}

	// 场景②：SWAPARB 策略，8 笔多数正（median>0）→ 不杀
	// nets = [+1,+1,+1,+1,+1,-0.5,-0.5,-0.5] → median=+1（排序后第4/5位=+1,+1 平均=+1）, avg=+0.4375
	// D-193-A 复审退回 R1：同场景①补 settlement 行。
	for i, realized := range []float64{1, 1, 1, 1, 1, -0.5, -0.5, -0.5} {
		var posID int64
		if err := s.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
			 VALUES ($1, 'SWAPARB', 'CLOSED', now(), now(), $2) RETURNING id`,
			"SWARB:keep:vA:vB:"+strconv.Itoa(i), realized).Scan(&posID); err != nil {
			t.Fatalf("insert keep pos %d: %v", i, err)
		}
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd)
			 VALUES ($1, 'SWAPARB:vA:vB', 10.0, $2, 1.0, now(), $2)`,
			posID, realized); err != nil {
			t.Fatalf("insert keep settlement pos %d: %v", i, err)
		}
	}
	e.runStartupElimination(ctx)

	disabledKeep, err := s.IsStrategyDisabled(ctx, "SWAPARB")
	if err != nil {
		t.Fatalf("IsStrategyDisabled SWAPARB: %v", err)
	}
	if disabledKeep {
		t.Fatal("SWAPARB 8 笔 median>0 → runStartupElimination 不应禁用（误杀保护）")
	}
}

// TestRunStartupElimination_EnvironmentIsolation D-200：runStartupElimination 按 e.cfg.Environment
// 聚合，跨 environment 的旧持仓不参与本环境淘汰判定。
// 场景：2 笔 SWAPARB CLOSED——posA environment='demo' net=-$1，posB environment='live' net=+$1。
// Engine.cfg.Environment='demo' → 只聚合 posA → TotalTrades=1 < 5（gate_decision.go:123 短路）→ 不禁用。
// 反例：Engine.cfg.Environment='live' → 只聚合 posB → TotalTrades=1 < 5 → 不禁用。
// 关键：若跨 environment 聚合（旧行为），TotalTrades=2 仍 <5 不禁用——但本测试同时验证聚合笔数
// 隔离生效（通过构造会触发淘汰的样本量见 TestRunStartupElimination_EnvIsolation_KillCase）。
func TestRunStartupElimination_EnvironmentIsolation(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats, autotrade_logs, positions, position_legs, position_cashflows, position_settlements CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// posA: environment='demo', net=-$1.0
	// posB: environment='live', net=+$1.0
	for _, p := range []struct {
		env, opp string
		net      float64
	}{
		{"demo", "SWARB:env:demo:vA:vB", -1.0},
		{"live", "SWARB:env:live:vA:vB", 1.0},
	} {
		var posID int64
		if err := s.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl, environment)
			 VALUES ($1, 'SWAPARB', 'CLOSED', now(), now(), $2, $3) RETURNING id`,
			p.opp, p.net, p.env).Scan(&posID); err != nil {
			t.Fatalf("insert pos env=%s: %v", p.env, err)
		}
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd)
			 VALUES ($1, 'SWAPARB:vA:vB', 10.0, $2, 1.0, now(), $2)`,
			posID, p.net); err != nil {
			t.Fatalf("insert settlement env=%s: %v", p.env, err)
		}
	}

	// 场景①：Engine.cfg.Environment='demo' → 只聚合 posA → TotalTrades=1 < 5 → 不禁用
	e := &Engine{store: s, cfg: gate.Config{Environment: "demo"}}
	e.runStartupElimination(ctx)
	disabled, err := s.IsStrategyDisabled(ctx, "SWAPARB")
	if err != nil {
		t.Fatalf("IsStrategyDisabled demo: %v", err)
	}
	if disabled {
		t.Fatal("demo environment 只聚合 1 笔（TotalTrades<5）→ 不应禁用 SWAPARB")
	}

	// 场景②（反例）：Engine.cfg.Environment='live' → 只聚合 posB → TotalTrades=1 < 5 → 不禁用
	// 先重置 disabled（场景①未禁用，无需重置），换 environment 再跑
	e2 := &Engine{store: s, cfg: gate.Config{Environment: "live"}}
	e2.runStartupElimination(ctx)
	disabledLive, err := s.IsStrategyDisabled(ctx, "SWAPARB")
	if err != nil {
		t.Fatalf("IsStrategyDisabled live: %v", err)
	}
	if disabledLive {
		t.Fatal("live environment 只聚合 1 笔（TotalTrades<5）→ 不应禁用 SWAPARB")
	}
}

// TestRunStartupElimination_EnvIsolation_KillCase D-200：environment 隔离下，本环境样本足量
// 且双负 → 仍触发淘汰；跨 environment 的正样本不稀释本环境判杀。
// 场景：demo environment 7 笔全负（median<0 avg<0，触发小样本双负淘汰）+ live environment 8 笔全正。
// Engine.cfg.Environment='demo' → 只聚合 demo 7 笔 → 判杀。旧行为（跨 environment）会聚合 15 笔，
// median 可能被 live 正样本拉正→不杀——本测试验证隔离后 demo 独立判杀。
func TestRunStartupElimination_EnvIsolation_KillCase(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats, autotrade_logs, positions, position_legs, position_cashflows, position_settlements CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// demo: 7 笔全负 → median<0 avg<0 → 小样本双负淘汰
	demoNets := []float64{-3, -3, -2, -1, -1, -0.5, -0.5}
	for i, net := range demoNets {
		var posID int64
		if err := s.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl, environment)
			 VALUES ($1, 'SWAPARB', 'CLOSED', now(), now(), $2, 'demo') RETURNING id`,
			"SWARB:kill:demo:vA:vB:"+strconv.Itoa(i), net).Scan(&posID); err != nil {
			t.Fatalf("insert demo pos %d: %v", i, err)
		}
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd)
			 VALUES ($1, 'SWAPARB:vA:vB', 10.0, $2, 1.0, now(), $2)`,
			posID, net); err != nil {
			t.Fatalf("insert demo settlement %d: %v", i, err)
		}
	}
	// live: 8 笔全正（跨 environment 干扰项）
	liveNets := []float64{1, 1, 1, 1, 1, 1, 1, 1}
	for i, net := range liveNets {
		var posID int64
		if err := s.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl, environment)
			 VALUES ($1, 'SWAPARB', 'CLOSED', now(), now(), $2, 'live') RETURNING id`,
			"SWARB:kill:live:vA:vB:"+strconv.Itoa(i), net).Scan(&posID); err != nil {
			t.Fatalf("insert live pos %d: %v", i, err)
		}
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd)
			 VALUES ($1, 'SWAPARB:vA:vB', 10.0, $2, 1.0, now(), $2)`,
			posID, net); err != nil {
			t.Fatalf("insert live settlement %d: %v", i, err)
		}
	}

	// Engine.cfg.Environment='demo' → 只聚合 demo 7 笔 → median<0 avg<0 → 判杀
	e := &Engine{store: s, cfg: gate.Config{Environment: "demo"}}
	e.runStartupElimination(ctx)
	disabled, err := s.IsStrategyDisabled(ctx, "SWAPARB")
	if err != nil {
		t.Fatalf("IsStrategyDisabled: %v", err)
	}
	if !disabled {
		t.Fatal("demo environment 7 笔双负 → 应判杀禁用 SWAPARB（live 正样本不应跨 environment 稀释）")
	}
}
