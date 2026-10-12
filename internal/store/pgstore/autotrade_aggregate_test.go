package pgstore

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// aggSeq 聚合测试 opp_id 唯一性计数器（避免同纳秒撞 opp_id）。
var aggSeq int64

// AggregateStrategyStats 真库测试（DSN-gated，照抄 swap_snapshots_test 模式）。
// fixture 造 5 仓（3 负 2 正含 cashflows 正负），断言 total/wins/avg/median。
// net 口径 = realized + Σcashflows（swap+funding+commission，commission 带负号）。

func testAggregateStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	ctx := context.Background()
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions, position_cashflows, position_legs, position_settlements CASCADE`); err != nil {
		t.Fatalf("TRUNCATE positions/cashflows/legs/settlements: %v", err)
	}
	return s
}

// insertAggPosition 插入一条 CLOSED 持仓（realized_pnl 非空）+ settlement 行，返回自增 id。
// AggregateStrategyStats 改读 position_settlements.net_realized_usd，
// fixture 需同步插 settlement 行（net_realized_usd 初始=realized，cashflow 插入时累加）。
func insertAggPosition(t *testing.T, s *Store, strategy string, realized float64) int64 {
	t.Helper()
	now := time.Now()
	seq := atomic.AddInt64(&aggSeq, 1)
	var id int64
	if err := s.pool.QueryRow(context.Background(),
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
		 VALUES ($1, $2, 'CLOSED', $3, $3, $4) RETURNING id`,
		"AGG:"+strategy+":vA:vB:"+now.Format("150405.000000")+"."+strconv.FormatInt(seq, 10), strategy, now, realized).Scan(&id); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	// 插 settlement 行：net_realized_usd 初始=realized（无 cashflow 时 = realized）
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd)
		 VALUES ($1, $2, 10.0, $3, 1.0, now(), $3)`,
		id, strategy+":vA:vB", realized); err != nil {
		t.Fatalf("insert settlement pos=%d: %v", id, err)
	}
	return id
}

// insertAggCashflow 插入一条现金流（amount_usd 带符号）并累加到 settlement.net_realized_usd。
// AggregateStrategyStats 读 net_realized_usd，cashflow 插入时同步累加保持 net=realized+Σcf 语义。
func insertAggCashflow(t *testing.T, s *Store, posID int64, kind string, amount float64) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO position_cashflows (position_id, kind, venue, amount_usd, ts)
		 VALUES ($1, $2, 'test', $3, now())`,
		posID, kind, amount); err != nil {
		t.Fatalf("insert cashflow pos=%d kind=%s: %v", posID, kind, err)
	}
	// 累加到 settlement.net_realized_usd（保持 net = realized + Σcashflows 口径）
	if _, err := s.pool.Exec(context.Background(),
		`UPDATE position_settlements SET net_realized_usd = COALESCE(net_realized_usd, realized_usd) + $2 WHERE position_id = $1`,
		posID, amount); err != nil {
		t.Fatalf("update settlement net pos=%d: %v", posID, err)
	}
}

// aggApprox 绝对误差容差（聚合测试本地 helper）。
func aggApprox(got, want, tol float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// TestAggregateStrategyStats_5Positions D-154 S5：5 仓（3 负 2 正含 cashflows）聚合。
// fixture（strategy=PERP_FUNDING）：
//
//	pos1: realized=-5, cf swap=+2  → net=-3 (负)
//	pos2: realized=-4, cf swap=+1  → net=-3 (负)
//	pos3: realized=-2, cf funding=+0.5 → net=-1.5 (负)
//	pos4: realized=+3, cf commission=-0.5 → net=+2.5 (正)
//	pos5: realized=+1, cf swap=+0.5 → net=+1.5 (正)
//
// nets = [-3, -3, -1.5, +2.5, +1.5] → total=5, wins=2
// avg = (-3-3-1.5+2.5+1.5)/5 = -3.5/5 = -0.7
// median（PG percentile_cont(0.5)，5 个奇数样本）= 排序后第 3 个 = -1.5
//
//	排序：[-3, -3, -1.5, 1.5, 2.5] → 中位 = -1.5
func TestAggregateStrategyStats_5Positions(t *testing.T) {
	s := testAggregateStore(t)
	ctx := context.Background()

	p1 := insertAggPosition(t, s, "PERP_FUNDING", -5)
	insertAggCashflow(t, s, p1, "swap", 2) // net=-3

	p2 := insertAggPosition(t, s, "PERP_FUNDING", -4)
	insertAggCashflow(t, s, p2, "swap", 1) // net=-3

	p3 := insertAggPosition(t, s, "PERP_FUNDING", -2)
	insertAggCashflow(t, s, p3, "funding", 0.5) // net=-1.5

	p4 := insertAggPosition(t, s, "PERP_FUNDING", 3)
	insertAggCashflow(t, s, p4, "commission", -0.5) // net=+2.5（commission 带负号）

	p5 := insertAggPosition(t, s, "PERP_FUNDING", 1)
	insertAggCashflow(t, s, p5, "swap", 0.5) // net=+1.5

	got, err := s.AggregateStrategyStats(ctx, "PERP_FUNDING")
	if err != nil {
		t.Fatalf("AggregateStrategyStats: %v", err)
	}
	if got.Total != 5 {
		t.Errorf("Total=%d, want 5", got.Total)
	}
	if got.Wins != 2 {
		t.Errorf("Wins=%d, want 2 (pos4/pos5 net>0)", got.Wins)
	}
	if !aggApprox(got.AvgPnl, -0.7, 0.001) {
		t.Errorf("AvgPnl=%.4f, want -0.7", got.AvgPnl)
	}
	if !aggApprox(got.MedianPnl, -1.5, 0.001) {
		t.Errorf("MedianPnl=%.4f, want -1.5 (PG percentile_cont 5 样本中位=第3个)", got.MedianPnl)
	}
}

// TestAggregateStrategyStats_EvenSamples D-154 S5：偶数样本 median=两中间值平均（PG percentile_cont 语义）。
// 4 仓 nets = [-4, -2, +1, +3] → median = (-2+1)/2 = -0.5
func TestAggregateStrategyStats_EvenSamples(t *testing.T) {
	s := testAggregateStore(t)
	ctx := context.Background()
	// realized 即 net（无 cashflow）
	insertAggPosition(t, s, "SWAPARB", -4)
	insertAggPosition(t, s, "SWAPARB", -2)
	insertAggPosition(t, s, "SWAPARB", 1)
	insertAggPosition(t, s, "SWAPARB", 3)

	got, err := s.AggregateStrategyStats(ctx, "SWAPARB")
	if err != nil {
		t.Fatalf("AggregateStrategyStats: %v", err)
	}
	if got.Total != 4 {
		t.Errorf("Total=%d, want 4", got.Total)
	}
	if got.Wins != 2 {
		t.Errorf("Wins=%d, want 2", got.Wins)
	}
	// median = (-2 + 1)/2 = -0.5（PG percentile_cont 偶数=两中间值线性插值，等距即平均）
	if !aggApprox(got.MedianPnl, -0.5, 0.001) {
		t.Errorf("MedianPnl=%.4f, want -0.5 (PG percentile_cont 4 样本=两中间值平均)", got.MedianPnl)
	}
}

// TestAggregateStrategyStats_ZeroPositions D-154 S5：0 笔 → total=0/wins=0/avg=0/median=0（COALESCE 兜底）。
func TestAggregateStrategyStats_ZeroPositions(t *testing.T) {
	s := testAggregateStore(t)
	ctx := context.Background()
	got, err := s.AggregateStrategyStats(ctx, "AGGEMPTY")
	if err != nil {
		t.Fatalf("AggregateStrategyStats: %v", err)
	}
	if got.Total != 0 || got.Wins != 0 || got.AvgPnl != 0 || got.MedianPnl != 0 {
		t.Errorf("0 笔聚合 = %+v, want 全 0（COALESCE 兜底 NULL→0）", got)
	}
}

// TestAggregateStrategyStats_RealizedNullExcluded D-154 S5：realized_pnl NULL 不计入（可判定口径）。
// 插 2 仓：1 笔 realized=5（计入），1 笔 realized=NULL（不计入）→ total=1。
func TestAggregateStrategyStats_RealizedNullExcluded(t *testing.T) {
	s := testAggregateStore(t)
	ctx := context.Background()
	// 计入仓
	insertAggPosition(t, s, "CARRY", 5)
	// NULL 仓（LEGGED 历史清理仓形态，realized_pnl NULL）
	now := time.Now()
	seq := atomic.AddInt64(&aggSeq, 1)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at)
		 VALUES ($1, 'CARRY', 'CLOSED', $2, $2)`,
		"CARRY:vA:vB:null."+strconv.FormatInt(seq, 10), now); err != nil {
		t.Fatalf("insert null position: %v", err)
	}
	got, err := s.AggregateStrategyStats(ctx, "CARRY")
	if err != nil {
		t.Fatalf("AggregateStrategyStats: %v", err)
	}
	if got.Total != 1 {
		t.Errorf("Total=%d, want 1（realized NULL 不计入可判定口径）", got.Total)
	}
	if !aggApprox(got.AvgPnl, 5.0, 0.001) {
		t.Errorf("AvgPnl=%.4f, want 5.0", got.AvgPnl)
	}
}

// TestDailyRealizedPnl_D193A_SettlementNet D-193-A 扩包 S2（决策 9）：
// settlement.net_realized_usd ≠ positions.realized_pnl 时取 settlement net。
// 红证据=改前 DailyRealizedPnl 读 realized_pnl，会取 10 而非 15（settlement net）。
func TestDailyRealizedPnl_D193A_SettlementNet(t *testing.T) {
	s := testAggregateStore(t)
	ctx := context.Background()
	now := time.Now()
	seq := atomic.AddInt64(&aggSeq, 1)
	var id int64
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
		 VALUES ($1, 'SWAPARB', 'CLOSED', $2, $2, 10.0) RETURNING id`,
		"DAILY:nettest:"+now.Format("150405.000000")+"."+strconv.FormatInt(seq, 10), now).Scan(&id); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	// settlement net_realized_usd=15（≠ realized_pnl=10）
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd)
		 VALUES ($1, 'k', 1.0, 10.0, 1.0, now(), 15.0)`, id); err != nil {
		t.Fatalf("insert settlement: %v", err)
	}
	got, err := s.DailyRealizedPnl(ctx)
	if err != nil {
		t.Fatalf("DailyRealizedPnl: %v", err)
	}
	// 决策 9：取 settlement net_realized_usd=15，非 realized_pnl=10
	if !aggApprox(got, 15.0, 0.001) {
		t.Errorf("DailyRealizedPnl=%.4f, want 15.0 (settlement net 优先于 realized_pnl)", got)
	}
}

// TestDailyRealizedPnl_D193A_NoSettlementFallback D-193-A 扩包 S2（决策 9）：
// 无 settlement 行（479 形态）→ fallback 到 realized_pnl。
func TestDailyRealizedPnl_D193A_NoSettlementFallback(t *testing.T) {
	s := testAggregateStore(t)
	ctx := context.Background()
	now := time.Now()
	seq := atomic.AddInt64(&aggSeq, 1)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
		 VALUES ($1, 'CARRY', 'CLOSED', $2, $2, -6.0)`,
		"DAILY:nosettle:"+now.Format("150405.000000")+"."+strconv.FormatInt(seq, 10), now); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	// 无 settlement 行（479 形态）
	got, err := s.DailyRealizedPnl(ctx)
	if err != nil {
		t.Fatalf("DailyRealizedPnl: %v", err)
	}
	// 决策 9：无 settlement → fallback realized_pnl=-6
	if !aggApprox(got, -6.0, 0.001) {
		t.Errorf("DailyRealizedPnl=%.4f, want -6.0 (无 settlement 行 fallback realized_pnl)", got)
	}
}

// TestDailyRealizedPnl_D193A_LegacySettlement D-193-A 扩包 S2（决策 9）：
// legacy settlement 行（net_realized_usd=NULL, realized_usd 有值）→ fallback 到 realized_usd。
func TestDailyRealizedPnl_D193A_LegacySettlement(t *testing.T) {
	s := testAggregateStore(t)
	ctx := context.Background()
	now := time.Now()
	seq := atomic.AddInt64(&aggSeq, 1)
	var id int64
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
		 VALUES ($1, 'SWAPARB', 'CLOSED', $2, $2, 8.0) RETURNING id`,
		"DAILY:legacy:"+now.Format("150405.000000")+"."+strconv.FormatInt(seq, 10), now).Scan(&id); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	// legacy settlement：net_realized_usd=NULL, realized_usd=12（≠ realized_pnl=8）
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at)
		 VALUES ($1, 'k', 1.0, 12.0, 1.0, now())`, id); err != nil {
		t.Fatalf("insert settlement: %v", err)
	}
	got, err := s.DailyRealizedPnl(ctx)
	if err != nil {
		t.Fatalf("DailyRealizedPnl: %v", err)
	}
	// 决策 9：net_realized_usd=NULL → fallback realized_usd=12（非 realized_pnl=8）
	if !aggApprox(got, 12.0, 0.001) {
		t.Errorf("DailyRealizedPnl=%.4f, want 12.0 (legacy settlement fallback realized_usd)", got)
	}
}
