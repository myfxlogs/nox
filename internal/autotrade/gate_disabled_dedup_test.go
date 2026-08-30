package autotrade

import (
	"context"
	"strconv"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// D-154 S5：gate disabled stdout 1h 去重 + 启动首跑聚合输入测试（真库 DSN-gated）。
// 复用 accumulator_test.go 的 testAccumStore helper。

// setupGateDisabledStore 构造真库 store：autotrade_state=RUNNING + strategy_stats disabled=true。
func setupGateDisabledStore(t *testing.T, strategy string) *pgstore.Store {
	t.Helper()
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats, autotrade_logs, positions, position_legs, position_cashflows CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// autotrade_state=RUNNING
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert autotrade_state: %v", err)
	}
	// strategy_stats disabled=true（策略禁用）
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO strategy_stats (strategy, disabled, disabled_reason, updated_at) VALUES ($1, true, 'test disabled', now())`,
		strategy); err != nil {
		t.Fatalf("insert strategy_stats: %v", err)
	}
	return s
}

// TestGateDisabled_Stdout1hDedup DBFull D-154 S3：disabled 拒绝同 strategy 1h 内 stdout 一次、DB 两次。
// 调 Submit 两次（disabled=true），断言：
//   - autotrade_logs 落库 2 行（DB 全量保留，审计完整性）
//   - lastDisabledLog 有该 strategy 记录（stdout 1h 去重生效，第二次 stdout 被压）
func TestGateDisabled_Stdout1hDedup(t *testing.T) {
	const strategy = "PERP_FUNDING"
	s := setupGateDisabledStore(t, strategy)
	ctx := context.Background()
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5})

	intent := TradeIntent{
		Strategy:  strategy,
		Canonical: "BTCUSDT",
		VenueBuy:  "okx:perp",
		VenueSell: "gate:perp",
		DedupKey:  "PERP_FUNDING:BTCUSDT:okx:perp:gate:perp",
		Execute:   func(ctx context.Context) (string, string, int64) { return "EXECUTED", "", 0 },
	}

	// 第一次 Submit：disabled 拒绝 → DB 落 1 行 + stdout 首 log
	g.Submit(ctx, intent)
	// 第二次 Submit：disabled 拒绝 → DB 落 1 行 + stdout 被去重压（1h 内）
	g.Submit(ctx, intent)

	// DB 全量断言：autotrade_logs 2 行 REJECTED
	var logCount int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM autotrade_logs WHERE strategy=$1 AND status='REJECTED'`, strategy).Scan(&logCount); err != nil {
		t.Fatalf("count autotrade_logs: %v", err)
	}
	if logCount != 2 {
		t.Errorf("autotrade_logs REJECTED 行数=%d, want 2（DB 全量保留，stdout 去重不影响落库）", logCount)
	}

	// stdout 去重断言：lastDisabledLog 有该 strategy 记录（第二次被 1h 窗口压）
	g.lastDisabledLogMu.Lock()
	last, ok := g.lastDisabledLog[strategy]
	g.lastDisabledLogMu.Unlock()
	if !ok {
		t.Fatal("lastDisabledLog 应含 strategy 记录（disabled stdout 去重已标记）")
	}
	if time.Since(last) >= time.Hour {
		t.Errorf("lastDisabledLog 距今=%v, 应 <1h（1h 去重窗口内）", time.Since(last))
	}
}

// TestGateDisabled_DifferentStrategiesNoDedup D-154 S3：不同 strategy 的 disabled 拒绝各自首报（per-key 去重）。
func TestGateDisabled_DifferentStrategiesNoDedup(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats, autotrade_logs, positions, position_legs, position_cashflows CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert autotrade_state: %v", err)
	}
	for _, st := range []string{"PERP_FUNDING", "MEANREVERT"} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO strategy_stats (strategy, disabled, disabled_reason, updated_at) VALUES ($1, true, 'test', now())`, st); err != nil {
			t.Fatalf("insert strategy_stats %s: %v", st, err)
		}
	}
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5})

	for _, st := range []string{"PERP_FUNDING", "MEANREVERT"} {
		intent := TradeIntent{
			Strategy:  st,
			Canonical: "BTCUSDT",
			VenueBuy:  "okx:perp",
			VenueSell: "gate:perp",
			DedupKey:  st + ":BTCUSDT:okx:perp:gate:perp",
			Execute:   func(ctx context.Context) (string, string, int64) { return "EXECUTED", "", 0 },
		}
		g.Submit(ctx, intent)
	}

	g.lastDisabledLogMu.Lock()
	count := len(g.lastDisabledLog)
	g.lastDisabledLogMu.Unlock()
	if count != 2 {
		t.Errorf("lastDisabledLog 条目数=%d, want 2（两不同 strategy 各自首报标记）", count)
	}
}

// TestGateDisabled_WindowExpiryResend D-154 返修②①：1h 窗口过后重发（内部状态断言法）。
// 手动塞 lastDisabledLog[strategy]=2h 前 → Submit 一次 disabled intent → 断言时间戳刷新为近 now
// （time.Since(last) < time.Minute）=窗口过期放行重发的代理证据。DB 全量不变（沿用 Stdout1hDedup 模式）。
func TestGateDisabled_WindowExpiryResend(t *testing.T) {
	const strategy = "PERP_FUNDING"
	s := setupGateDisabledStore(t, strategy)
	ctx := context.Background()
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5})

	// 手动塞 2h 前的时间戳（模拟窗口已过期）
	g.lastDisabledLogMu.Lock()
	g.lastDisabledLog[strategy] = time.Now().Add(-2 * time.Hour)
	g.lastDisabledLogMu.Unlock()

	intent := TradeIntent{
		Strategy:  strategy,
		Canonical: "BTCUSDT",
		VenueBuy:  "okx:perp",
		VenueSell: "gate:perp",
		DedupKey:  "PERP_FUNDING:BTCUSDT:okx:perp:gate:perp",
		Execute:   func(ctx context.Context) (string, string, int64) { return "EXECUTED", "", 0 },
	}

	// Submit：窗口过期 → stdout 放行重发 + 时间戳刷新为 now
	g.Submit(ctx, intent)

	// 内部状态断言：时间戳应刷新为近 now（<1min），证明窗口过期后重发
	g.lastDisabledLogMu.Lock()
	last, ok := g.lastDisabledLog[strategy]
	g.lastDisabledLogMu.Unlock()
	if !ok {
		t.Fatal("lastDisabledLog 应含 strategy 记录")
	}
	if time.Since(last) >= time.Minute {
		t.Errorf("lastDisabledLog 距今=%v, 应 <1min（窗口过期后时间戳应刷新为 now）", time.Since(last))
	}

	// DB 全量断言：autotrade_logs 1 行 REJECTED（窗口过期不影响落库）
	var logCount int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM autotrade_logs WHERE strategy=$1 AND status='REJECTED'`, strategy).Scan(&logCount); err != nil {
		t.Fatalf("count autotrade_logs: %v", err)
	}
	if logCount != 1 {
		t.Errorf("autotrade_logs REJECTED 行数=%d, want 1（DB 全量保留，窗口过期不影响落库）", logCount)
	}
}

// TestRunStartupElimination_AggregateInput D-154 裁决①补 S5：聚合输入下启动首跑判杀/不杀。
// 场景①（判杀）：插 7 笔 CLOSED 仓（median<0 avg<0）→ runStartupElimination 应 DisableStrategy。
// 场景②（不杀）：插 8 笔 CLOSED 仓（median>0）→ 不禁用。
// 用真库聚合验证 runStartupElimination 走 AggregateStrategyStats 而非 strategy_stats 累计列。
// 策略名用真实 CHECK 约束允许值（PERP_FUNDING 判杀 / SWAPARB 不杀）。
func TestRunStartupElimination_AggregateInput(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats, autotrade_logs, positions, position_legs, position_cashflows CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// 场景①：PERP_FUNDING 策略，7 笔全负（median<0 avg<0）→ 判杀
	// nets = [-3,-3,-2,-1,-1,-0.5,-0.5] → median=-1, avg=-1.57
	for i, realized := range []float64{-3, -3, -2, -1, -1, -0.5, -0.5} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
			 VALUES ($1, 'PERP_FUNDING', 'CLOSED', now(), now(), $2)`,
			"PFUND:kill:vA:vB:"+strconv.Itoa(i), realized); err != nil {
			t.Fatalf("insert pos %d: %v", i, err)
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
	for i, realized := range []float64{1, 1, 1, 1, 1, -0.5, -0.5, -0.5} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
			 VALUES ($1, 'SWAPARB', 'CLOSED', now(), now(), $2)`,
			"SWARB:keep:vA:vB:"+strconv.Itoa(i), realized); err != nil {
			t.Fatalf("insert keep pos %d: %v", i, err)
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
