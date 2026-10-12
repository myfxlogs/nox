package pgstore

// D-323：结算聚合 stats_status 过滤锚（T1-T4）——D-193-A 设计意图「统计层不消费 estimated」
// 的落地证明：applied+NULL(legacy) 计入、skipped/retryable/pending 出局。
// 先红后绿：对现行码（无过滤）T1/T3/T4 红（skipped 行计入）；T2 恒绿（兼容锚，防过滤写成等值比较）。

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// insertSettledPosWithStatus D-323 播种 helper：CLOSED 仓 + settlement 行（net=realized）+显式 stats_status/environment。
// statsStatus 传 nil = NULL（legacy 形态）。
func insertSettledPosWithStatus(t *testing.T, s *Store, strategy, environment string, statsStatus any, realized float64) int64 {
	t.Helper()
	now := time.Now()
	seq := atomic.AddInt64(&aggSeq, 1)
	var id int64
	if err := s.pool.QueryRow(context.Background(),
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl, environment)
		 VALUES ($1, $2, 'CLOSED', $3, $3, $4, $5) RETURNING id`,
		"AGG:"+strategy+":vA:vB:"+now.Format("150405.000000")+"."+strconv.FormatInt(seq, 10), strategy, now, realized, environment).Scan(&id); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd, stats_status)
		 VALUES ($1, $2, 10.0, $3, 1.0, now(), $3, $4)`,
		id, strategy+":vA:vB", realized, statsStatus); err != nil {
		t.Fatalf("insert settlement pos=%d: %v", id, err)
	}
	return id
}

// TestAggregateStrategyStats_SkippedExcluded T1（主锚）：skipped 行出局。
// applied net=+5 + skipped net=−99 → total=1、median=+5（skipped 不计——现行码 total=2/median=−47 红）。
func TestAggregateStrategyStats_SkippedExcluded(t *testing.T) {
	s := testAggregateStore(t)
	ctx := context.Background()
	insertSettledPosWithStatus(t, s, "PERP_FUNDING", "", "applied", 5)
	insertSettledPosWithStatus(t, s, "PERP_FUNDING", "", "skipped", -99)

	got, err := s.AggregateStrategyStats(ctx, "PERP_FUNDING")
	if err != nil {
		t.Fatalf("AggregateStrategyStats: %v", err)
	}
	if got.Total != 1 {
		t.Errorf("Total=%d, want 1（skipped 行不计入淘汰真相源）", got.Total)
	}
	if got.Wins != 1 {
		t.Errorf("Wins=%d, want 1", got.Wins)
	}
	if !aggApprox(got.MedianPnl, 5.0, 0.001) {
		t.Errorf("MedianPnl=%.4f, want 5.0（skipped −99 出局）", got.MedianPnl)
	}
	if !aggApprox(got.AvgPnl, 5.0, 0.001) {
		t.Errorf("AvgPnl=%.4f, want 5.0", got.AvgPnl)
	}
}

// TestAggregateStrategyStats_NullStatsStatusIncluded T2（兼容锚）：stats_status=NULL 行计入（legacy 口径）。
// 列可空（migration 0055 无 NOT NULL），NULL=legacy 行与 D-193-A §3.6 NULL→actual 同口径。
// 恒绿锚：防过滤写成 `stats_status='applied'` 等值比较（那会误杀 legacy 行）——变异见对抗记录。
func TestAggregateStrategyStats_NullStatsStatusIncluded(t *testing.T) {
	s := testAggregateStore(t)
	ctx := context.Background()
	insertSettledPosWithStatus(t, s, "CARRY", "", nil, 5) // legacy NULL 行
	insertSettledPosWithStatus(t, s, "CARRY", "", "applied", -2)

	got, err := s.AggregateStrategyStats(ctx, "CARRY")
	if err != nil {
		t.Fatalf("AggregateStrategyStats: %v", err)
	}
	if got.Total != 2 {
		t.Errorf("Total=%d, want 2（NULL legacy 行必须计入）", got.Total)
	}
	// median = (-2+5)/2 = 1.5（PG percentile_cont 偶数=两中间值平均）
	if !aggApprox(got.MedianPnl, 1.5, 0.001) {
		t.Errorf("MedianPnl=%.4f, want 1.5", got.MedianPnl)
	}
}

// TestAggregateStrategyStats_RetryablePendingExcluded T3（边界锚）：retryable/pending 同样出局。
// applied +5 计入；retryable −7、pending −8 出局 → total=1、median=+5（现行码 total=3 红）。
func TestAggregateStrategyStats_RetryablePendingExcluded(t *testing.T) {
	s := testAggregateStore(t)
	ctx := context.Background()
	insertSettledPosWithStatus(t, s, "SWAPARB", "", "applied", 5)
	insertSettledPosWithStatus(t, s, "SWAPARB", "", "retryable", -7)
	insertSettledPosWithStatus(t, s, "SWAPARB", "", "pending", -8)

	got, err := s.AggregateStrategyStats(ctx, "SWAPARB")
	if err != nil {
		t.Fatalf("AggregateStrategyStats: %v", err)
	}
	if got.Total != 1 {
		t.Errorf("Total=%d, want 1（retryable/pending 行不计入）", got.Total)
	}
	if !aggApprox(got.MedianPnl, 5.0, 0.001) {
		t.Errorf("MedianPnl=%.4f, want 5.0", got.MedianPnl)
	}
}

// TestAggregateStatsStatusFilter_ByEnvAndFamily T4（覆盖锚）：三函数同一 fixture 各跑一遍。
// applied +5（environment=demo）+ skipped −99（environment=demo）→ 各聚合均 total=1、median=+5：
// ByEnvironment("") 等价路径 + ByEnvironment("demo") 指定 env 路径 + Family 族路径同规则（现行码 total=2 红）。
func TestAggregateStatsStatusFilter_ByEnvAndFamily(t *testing.T) {
	s := testAggregateStore(t)
	ctx := context.Background()
	insertSettledPosWithStatus(t, s, "CARRY", "demo", "applied", 5)
	insertSettledPosWithStatus(t, s, "CARRY", "demo", "skipped", -99)

	// ① ByEnvironment("") 等价路径
	got, err := s.AggregateStrategyStatsByEnvironment(ctx, "CARRY", "")
	if err != nil {
		t.Fatalf("ByEnvironment(\":\"): %v", err)
	}
	if got.Total != 1 || !aggApprox(got.MedianPnl, 5.0, 0.001) {
		t.Errorf("ByEnvironment(\"\") = total %d median %.4f, want 1 / 5.0（skipped 出局）", got.Total, got.MedianPnl)
	}

	// ② ByEnvironment("demo") 指定 env 路径
	got, err = s.AggregateStrategyStatsByEnvironment(ctx, "CARRY", "demo")
	if err != nil {
		t.Fatalf("ByEnvironment(demo): %v", err)
	}
	if got.Total != 1 || !aggApprox(got.MedianPnl, 5.0, 0.001) {
		t.Errorf("ByEnvironment(demo) = total %d median %.4f, want 1 / 5.0", got.Total, got.MedianPnl)
	}

	// ③ Family 族路径
	got, err = s.AggregateStrategyStatsByEnvironmentFamily(ctx, []string{"CARRY", "SWAPARB"}, "")
	if err != nil {
		t.Fatalf("ByEnvironmentFamily: %v", err)
	}
	if got.Total != 1 || !aggApprox(got.MedianPnl, 5.0, 0.001) {
		t.Errorf("ByEnvironmentFamily = total %d median %.4f, want 1 / 5.0", got.Total, got.MedianPnl)
	}
}
