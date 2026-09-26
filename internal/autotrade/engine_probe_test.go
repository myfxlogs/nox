// engine_probe_test.go —— D-181 S2：probe 探测仓评估单测。
package autotrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// setupProbeStore 构造真库 store + position + legs + swap snapshots。
// openedAtDaysAgo：持仓开仓时间距今天数。
// swapJumps：每腿的 swap 快照序列（已按 ts 升序）。
func setupProbeStore(t *testing.T, openedAtDaysAgo int, legs []probeLegSeed, snaps []probeSnapSeed) *pgstore.Store {
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
	for _, tbl := range []string{"position_swap_snapshots", "position_legs", "positions", "facts", "broker_accounts", "autotrade_state"} {
		if _, err := s.Pool().Exec(ctx, "TRUNCATE "+tbl+" CASCADE"); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert autotrade_state: %v", err)
	}
	openedAt := time.Now().UTC().Add(-time.Duration(openedAtDaysAgo) * 24 * time.Hour)
	var posID int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, note)
		 VALUES ('SWAPARB:AUDJPY:vA:vB', 'SWAPARB', 'OPEN', $1, 'PROBE') RETURNING id`,
		openedAt).Scan(&posID); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	for i, leg := range legs {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
			 VALUES ($1, $2, 'OPEN', split_part($3,':',1), split_part($3,':',2), 'AUDJPY', $4, $5, 100, 'FILLED')`,
			posID, i, leg.venue, leg.side, leg.qty); err != nil {
			t.Fatalf("insert position_leg %d: %v", i, err)
		}
	}
	for _, snap := range snaps {
		if err := s.InsertSwapSnapshot(ctx, posID, snap.venue, snap.swapUSD, snap.ts); err != nil {
			t.Fatalf("insert swap snapshot: %v", err)
		}
	}
	return s
}

type probeLegSeed struct {
	venue string
	side  string
	qty   float64
}

type probeSnapSeed struct {
	venue   string
	swapUSD float64
	ts      time.Time
}

// TestEvaluateProbe_Upgrade 净结算 > 0 → 升级（upgraded=true）。
// 两腿各 0.01 lot，开仓 5 天前，swap 快照：开仓 0 → 第 3 天 +0.10（结算事件）。
// netUsdLotDay = 0.10 / 0.02 / 5 = 1.0 > 0 → 升级。
func TestEvaluateProbe_Upgrade(t *testing.T) {
	openedAt := time.Now().UTC().Add(-5 * 24 * time.Hour)
	s := setupProbeStore(t, 5,
		[]probeLegSeed{{"vA:mt5", "BUY", 0.01}, {"vB:mt5", "SELL", 0.01}},
		[]probeSnapSeed{
			{"vA:mt5", 0, openedAt},
			{"vA:mt5", 0.10, openedAt.Add(3 * 24 * time.Hour)},
			{"vB:mt5", 0, openedAt},
			{"vB:mt5", 0.10, openedAt.Add(3 * 24 * time.Hour)},
		},
	)
	e := &Engine{store: s, cfg: Config{ProbeEnabled: true, ProbeEvalDays: 3}}
	pos, err := s.GetPosition(context.Background(), mustGetPosID(t, s))
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}
	res := e.evaluateProbe(context.Background(), pos)
	if !res.upgraded {
		t.Errorf("want upgraded=true, got %+v", res)
	}
	if res.shouldClose {
		t.Errorf("want shouldClose=false, got true")
	}
}

// TestEvaluateProbe_RejectNegative 净结算 < 0 → 平仓拒绝。
func TestEvaluateProbe_RejectNegative(t *testing.T) {
	openedAt := time.Now().UTC().Add(-5 * 24 * time.Hour)
	s := setupProbeStore(t, 5,
		[]probeLegSeed{{"vA:mt5", "BUY", 0.01}, {"vB:mt5", "SELL", 0.01}},
		[]probeSnapSeed{
			{"vA:mt5", 0, openedAt},
			{"vA:mt5", -0.10, openedAt.Add(3 * 24 * time.Hour)},
			{"vB:mt5", 0, openedAt},
			{"vB:mt5", -0.10, openedAt.Add(3 * 24 * time.Hour)},
		},
	)
	e := &Engine{store: s, cfg: Config{ProbeEnabled: true, ProbeEvalDays: 3}}
	pos, err := s.GetPosition(context.Background(), mustGetPosID(t, s))
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}
	res := e.evaluateProbe(context.Background(), pos)
	if !res.shouldClose {
		t.Errorf("want shouldClose=true, got %+v", res)
	}
	if !strings.Contains(res.reason, "probe rejected") {
		t.Errorf("reason should contain 'probe rejected', got %q", res.reason)
	}
}

// TestEvaluateProbe_RejectZeroSettlement 无跳变 → 平仓拒绝（实测净零）。
func TestEvaluateProbe_RejectZeroSettlement(t *testing.T) {
	openedAt := time.Now().UTC().Add(-5 * 24 * time.Hour)
	s := setupProbeStore(t, 5,
		[]probeLegSeed{{"vA:mt5", "BUY", 0.01}, {"vB:mt5", "SELL", 0.01}},
		[]probeSnapSeed{
			{"vA:mt5", 0, openedAt},
			{"vA:mt5", 0, openedAt.Add(3 * 24 * time.Hour)}, // 无跳变
			{"vB:mt5", 0, openedAt},
			{"vB:mt5", 0, openedAt.Add(3 * 24 * time.Hour)},
		},
	)
	e := &Engine{store: s, cfg: Config{ProbeEnabled: true, ProbeEvalDays: 3}}
	pos, err := s.GetPosition(context.Background(), mustGetPosID(t, s))
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}
	res := e.evaluateProbe(context.Background(), pos)
	if !res.shouldClose {
		t.Errorf("want shouldClose=true (zero settlement), got %+v", res)
	}
	if !strings.Contains(res.reason, "zero settlement") {
		t.Errorf("reason should contain 'zero settlement', got %q", res.reason)
	}
}

// TestEvaluateProbe_NotExpired 未到期 → 不评估。
func TestEvaluateProbe_NotExpired(t *testing.T) {
	s := setupProbeStore(t, 1, // 1 天前开仓，未到 3 天
		[]probeLegSeed{{"vA:mt5", "BUY", 0.01}, {"vB:mt5", "SELL", 0.01}},
		nil,
	)
	e := &Engine{store: s, cfg: Config{ProbeEnabled: true, ProbeEvalDays: 3}}
	pos, err := s.GetPosition(context.Background(), mustGetPosID(t, s))
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}
	res := e.evaluateProbe(context.Background(), pos)
	if res.shouldClose || res.upgraded {
		t.Errorf("not expired: want no action, got %+v", res)
	}
}

// TestEvaluateProbe_Disabled ProbeEnabled=false → 不评估。
func TestEvaluateProbe_Disabled(t *testing.T) {
	s := setupProbeStore(t, 5,
		[]probeLegSeed{{"vA:mt5", "BUY", 0.01}, {"vB:mt5", "SELL", 0.01}},
		nil,
	)
	e := &Engine{store: s, cfg: Config{ProbeEnabled: false, ProbeEvalDays: 3}}
	pos, err := s.GetPosition(context.Background(), mustGetPosID(t, s))
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}
	res := e.evaluateProbe(context.Background(), pos)
	if res.shouldClose || res.upgraded {
		t.Errorf("disabled: want no action, got %+v", res)
	}
}

// TestEvaluateProbe_NotProbeNote note 非 PROBE → 不评估。
func TestEvaluateProbe_NotProbeNote(t *testing.T) {
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
	if _, err := s.Pool().Exec(ctx, "TRUNCATE positions CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	openedAt := time.Now().UTC().Add(-5 * 24 * time.Hour)
	var posID int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, note)
		 VALUES ('SWAPARB:AUDJPY:vA:vB', 'SWAPARB', 'OPEN', $1, '') RETURNING id`,
		openedAt).Scan(&posID); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	e := &Engine{store: s, cfg: Config{ProbeEnabled: true, ProbeEvalDays: 3}}
	pos, err := s.GetPosition(ctx, posID)
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}
	res := e.evaluateProbe(ctx, pos)
	if res.shouldClose || res.upgraded {
		t.Errorf("non-PROBE note: want no action, got %+v", res)
	}
}

// TestSumSwapJumpsSince 纯函数测试。
func TestSumSwapJumpsSince(t *testing.T) {
	since := time.Now().Add(-5 * 24 * time.Hour)
	snaps := []pgstore.SwapSnapshot{
		{Ts: since.Add(-1 * time.Hour), SwapUSD: 0},        // before since
		{Ts: since.Add(1 * time.Hour), SwapUSD: 0.10},      // jump +0.10
		{Ts: since.Add(2 * 24 * time.Hour), SwapUSD: 0.15}, // jump +0.05
	}
	total, hasJump := sumSwapJumpsSince(snaps, since)
	if !hasJump {
		t.Error("want hasJump=true")
	}
	if total != 0.15 {
		t.Errorf("total = %.4f, want 0.15", total)
	}
}

// TestComboAllowVerdict_ProbeNotCounted D-181 S2：probe 仓不计 MAX_ACTIVE_COMBOS。
func TestComboAllowVerdict_ProbeNotCounted(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "AUDJPY", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	// 1 个常规仓 + 1 个 probe 仓，maxActive=1 → 常规仓计数 1 >= 1 应拒，
	// 但 probe 仓不计 → activeCount=1（常规）>= 1 → 仍拒。
	// 改为 maxActive=2 → activeCount=1（常规）< 2 → 放行（probe 不占额度）。
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:AUDJPY:A:mt5:B:mt5", Note: ""},
		{OpportunityID: "SWAPARB:AUDJPY:A:mt5:B:mt5", Note: "PROBE"},
	}
	v := comboAllowVerdict(nil, nil, true, 2, 0, intent, active)
	if !v.Pass {
		t.Errorf("probe not counted: want pass (activeCount=1 < maxActive=2), got reject: %s", v.Reason)
	}
}

// TestComboAllowVerdict_RegularStillCounted D-181 S2 反证：常规仓仍计数。
func TestComboAllowVerdict_RegularStillCounted(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "AUDJPY", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:AUDJPY:A:mt5:B:mt5", Note: ""}, // 常规仓计数
	}
	v := comboAllowVerdict(nil, nil, true, 1, 0, intent, active)
	if v.Pass {
		t.Error("regular counted: want reject (activeCount=1 >= maxActive=1), got pass")
	}
}

// mustGetPosID 从 store 取第一个 position ID（测试辅助）。
func mustGetPosID(t *testing.T, s *pgstore.Store) int64 {
	t.Helper()
	var posID int64
	err := s.Pool().QueryRow(context.Background(), "SELECT id FROM positions LIMIT 1").Scan(&posID)
	if err != nil {
		t.Fatalf("get posID: %v", err)
	}
	return posID
}

// 确保 fact 包被引用（setupProbeStore 间接用）。
var _ = fact.KindSwapLong
