// 测试：fin_trx_vote_rebalance Store 方法（DSN 真实夹具）。
package pgstore

import (
	"context"
	"testing"
	"time"
)

// trxVoteRebalanceTestStore 连接 NOX_TEST_PG_DSN 测试库，TRUNCATE fin_trx_vote_rebalance 隔离数据。
func trxVoteRebalanceTestStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	ctx := context.Background()
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_trx_vote_rebalance CASCADE"); err != nil {
		t.Fatalf("TRUNCATE fin_trx_vote_rebalance: %v", err)
	}
	return s
}

// TestTrxVoteRebalance_InsertAndGetLast Insert + GetLast 往返。
func TestTrxVoteRebalance_InsertAndGetLast(t *testing.T) {
	s := trxVoteRebalanceTestStore(t)
	ctx := context.Background()
	rec := TRXVoteRebalance{
		CurrentYield:   100.0,
		TargetYield:    120.0,
		ImprovementPct: 20.0,
		NeedRebalance:  true,
		Reason:         "better opp +20%",
		TargetVotes:    []byte(`[{"address":"Tabc","votes":5000}]`),
		Status:         "pending",
	}
	id, err := s.InsertTrxVoteRebalance(ctx, rec)
	if err != nil {
		t.Fatalf("InsertTrxVoteRebalance: %v", err)
	}
	if id <= 0 {
		t.Fatalf("id = %d, want > 0", id)
	}
	// GetLast
	last, err := s.GetLastTrxVoteRebalance(ctx)
	if err != nil {
		t.Fatalf("GetLastTrxVoteRebalance: %v", err)
	}
	if last.ID != id {
		t.Errorf("ID = %d, want %d", last.ID, id)
	}
	if last.CurrentYield != 100.0 {
		t.Errorf("CurrentYield = %v, want 100.0", last.CurrentYield)
	}
	if last.TargetYield != 120.0 {
		t.Errorf("TargetYield = %v, want 120.0", last.TargetYield)
	}
	if last.ImprovementPct != 20.0 {
		t.Errorf("ImprovementPct = %v, want 20.0", last.ImprovementPct)
	}
	if !last.NeedRebalance {
		t.Error("NeedRebalance = false, want true")
	}
	if last.Reason != "better opp +20%" {
		t.Errorf("Reason = %q, want 'better opp +20%%'", last.Reason)
	}
	if last.Status != "pending" {
		t.Errorf("Status = %q, want pending", last.Status)
	}
	// executed_at / tx_hash 应为 nil（未执行）
	if last.ExecutedAt != nil {
		t.Errorf("ExecutedAt = %v, want nil (未执行)", last.ExecutedAt)
	}
	if last.TxHash != nil {
		t.Errorf("TxHash = %v, want nil (未执行)", *last.TxHash)
	}
}

// TestTrxVoteRebalancedToday 今日有 executed=true。
func TestTrxVoteRebalancedToday(t *testing.T) {
	s := trxVoteRebalanceTestStore(t)
	ctx := context.Background()
	// 未插入 → false
	if s.TrxVoteRebalancedToday(ctx, 1) {
		t.Error("empty: TrxVoteRebalancedToday = true, want false")
	}
	// 插入 1 条 pending → false（pending 不算已执行）
	rec := TRXVoteRebalance{
		CurrentYield: 100.0, TargetYield: 120.0, ImprovementPct: 20.0,
		NeedRebalance: true, Reason: "test", TargetVotes: []byte(`[]`),
		Status: "pending",
	}
	id, err := s.InsertTrxVoteRebalance(ctx, rec)
	if err != nil {
		t.Fatalf("Insert pending: %v", err)
	}
	if s.TrxVoteRebalancedToday(ctx, 1) {
		t.Error("pending only: TrxVoteRebalancedToday = true, want false")
	}
	// UpdateTrxVoteRebalanceResult → status=executed → true
	if err := s.UpdateTrxVoteRebalanceResult(ctx, id, "deadbeef", "executed"); err != nil {
		t.Fatalf("UpdateTrxVoteRebalanceResult: %v", err)
	}
	if !s.TrxVoteRebalancedToday(ctx, 1) {
		t.Error("after executed: TrxVoteRebalancedToday = false, want true")
	}
}

// TestTrxVoteRebalancedToday_NoExecution 今日有 pending 但无 executed → false。
func TestTrxVoteRebalancedToday_NoExecution(t *testing.T) {
	s := trxVoteRebalanceTestStore(t)
	ctx := context.Background()
	rec := TRXVoteRebalance{
		CurrentYield: 100.0, TargetYield: 100.0, ImprovementPct: 0.0,
		NeedRebalance: false, Reason: "no change", TargetVotes: []byte(`[]`),
		Status: "skipped",
	}
	if _, err := s.InsertTrxVoteRebalance(ctx, rec); err != nil {
		t.Fatalf("Insert skipped: %v", err)
	}
	if s.TrxVoteRebalancedToday(ctx, 1) {
		t.Error("skipped only: TrxVoteRebalancedToday = true, want false")
	}
}

// TestTrxVoteRebalancedToday_MaxPerDay maxPerDay 参数：插入 2 条 executed，
// maxPerDay=1 返回 true（达上限），maxPerDay=2 返回 false（未达上限）。
func TestTrxVoteRebalancedToday_MaxPerDay(t *testing.T) {
	s := trxVoteRebalanceTestStore(t)
	ctx := context.Background()
	// 插入 2 条 executed
	for i := 0; i < 2; i++ {
		rec := TRXVoteRebalance{
			CurrentYield: 100.0, TargetYield: 120.0, ImprovementPct: 20.0,
			NeedRebalance: true, Reason: "test", TargetVotes: []byte(`[]`),
			Status: "pending",
		}
		id, err := s.InsertTrxVoteRebalance(ctx, rec)
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
		if err := s.UpdateTrxVoteRebalanceResult(ctx, id, "deadbeef", "executed"); err != nil {
			t.Fatalf("Update %d: %v", i, err)
		}
	}
	// maxPerDay=1 → count=2 >= 1 → true（达上限）
	if !s.TrxVoteRebalancedToday(ctx, 1) {
		t.Error("maxPerDay=1: TrxVoteRebalancedToday = false, want true (2 executed >= 1)")
	}
	// maxPerDay=2 → count=2 >= 2 → true（达上限）
	if !s.TrxVoteRebalancedToday(ctx, 2) {
		t.Error("maxPerDay=2: TrxVoteRebalancedToday = false, want true (2 executed >= 2)")
	}
	// maxPerDay=3 → count=2 < 3 → false（未达上限）
	if s.TrxVoteRebalancedToday(ctx, 3) {
		t.Error("maxPerDay=3: TrxVoteRebalancedToday = true, want false (2 executed < 3)")
	}
}

// TestUpdateTrxVoteRebalanceResult 状态流转：pending → executed（tx_hash 回填）。
func TestUpdateTrxVoteRebalanceResult(t *testing.T) {
	s := trxVoteRebalanceTestStore(t)
	ctx := context.Background()
	rec := TRXVoteRebalance{
		CurrentYield: 100.0, TargetYield: 120.0, ImprovementPct: 20.0,
		NeedRebalance: true, Reason: "test", TargetVotes: []byte(`[]`),
		Status: "pending",
	}
	id, err := s.InsertTrxVoteRebalance(ctx, rec)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Update → executed
	if err := s.UpdateTrxVoteRebalanceResult(ctx, id, "deadbeef1234", "executed"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// GetLast 验证回填
	last, err := s.GetLastTrxVoteRebalance(ctx)
	if err != nil {
		t.Fatalf("GetLast: %v", err)
	}
	if last.Status != "executed" {
		t.Errorf("Status = %q, want executed", last.Status)
	}
	if last.TxHash == nil || *last.TxHash != "deadbeef1234" {
		t.Errorf("TxHash = %v, want deadbeef1234", last.TxHash)
	}
	if last.ExecutedAt == nil {
		t.Error("ExecutedAt = nil, want non-nil (已执行)")
	}
	// executed_at 应在最近 1 分钟内
	if last.ExecutedAt != nil && time.Since(*last.ExecutedAt) > time.Minute {
		t.Errorf("ExecutedAt = %v, want recent (within 1 min)", *last.ExecutedAt)
	}
}
