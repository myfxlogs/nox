// D-249 S3 测试：TrxVoteAprRefresh handler——kick scheduler + 返回最新聚合行。
// 先红后绿：handler 未实现前编译失败；实现后验证 kick 调用 + 字段透传 + 空表容忍 + nil kickFn 安全。
package dashboard

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"connectrpc.com/connect"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// TestD249_TrxVoteAprRefresh_KickAndReturn 验证 handler 调 kickFn("trx_apr") 后返回最新行。
func TestD249_TrxVoteAprRefresh_KickAndReturn(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	maxSr := "SR-Luganodes"
	curSr := "SR-TronSpark"
	if err := s.InsertTRXVoteApr(ctx, pgstore.TRXVoteApr{
		FetchedAt: now, CurrentRate: 2.85, CurrentSr: &curSr,
		MaxRate: 3.05, MaxSr: &maxSr, Spread: 0.20,
	}); err != nil {
		t.Fatalf("InsertTRXVoteApr: %v", err)
	}
	var kickedName string
	var kickCalls int32
	svc := &Service{
		store:  s,
		kickFn: func(name string) { atomic.AddInt32(&kickCalls, 1); kickedName = name },
	}
	resp, err := svc.TrxVoteAprRefresh(ctx, connect.NewRequest(&v1.TrxVoteAprRefreshRequest{}))
	if err != nil {
		t.Fatalf("TrxVoteAprRefresh: %v", err)
	}
	if atomic.LoadInt32(&kickCalls) != 1 {
		t.Errorf("kickCalls = %d, want 1", kickCalls)
	}
	if kickedName != "trx_apr" {
		t.Errorf("kickedName = %q, want trx_apr", kickedName)
	}
	if resp.Msg.CurrentRate != 2.85 {
		t.Errorf("current_rate = %v, want 2.85", resp.Msg.CurrentRate)
	}
	if resp.Msg.MaxRate != 3.05 {
		t.Errorf("max_rate = %v, want 3.05", resp.Msg.MaxRate)
	}
	if resp.Msg.Spread != 0.20 {
		t.Errorf("spread = %v, want 0.20", resp.Msg.Spread)
	}
	if resp.Msg.CurrentSr != curSr {
		t.Errorf("current_sr = %q, want %s", resp.Msg.CurrentSr, curSr)
	}
	if resp.Msg.MaxSr != maxSr {
		t.Errorf("max_sr = %q, want %s", resp.Msg.MaxSr, maxSr)
	}
	if resp.Msg.FetchedAt == nil {
		t.Error("fetched_at = nil, want timestamp")
	}
}

// TestD249_TrxVoteAprRefresh_NilKickFn 验证 kickFn nil 时不 panic（测试/未接线场景）。
func TestD249_TrxVoteAprRefresh_NilKickFn(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.InsertTRXVoteApr(ctx, pgstore.TRXVoteApr{
		FetchedAt: now, CurrentRate: 1.0, MaxRate: 2.0, Spread: 1.0,
	}); err != nil {
		t.Fatalf("InsertTRXVoteApr: %v", err)
	}
	svc := &Service{store: s} // kickFn nil
	resp, err := svc.TrxVoteAprRefresh(ctx, connect.NewRequest(&v1.TrxVoteAprRefreshRequest{}))
	if err != nil {
		t.Fatalf("TrxVoteAprRefresh nil kickFn: %v", err)
	}
	if resp.Msg.CurrentRate != 1.0 {
		t.Errorf("current_rate = %v, want 1.0", resp.Msg.CurrentRate)
	}
}

// TestD249_TrxVoteAprRefresh_EmptyTable 验证空表返回零值响应（不报错）。
func TestD249_TrxVoteAprRefresh_EmptyTable(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	var kickCalls int32
	svc := &Service{
		store:  s,
		kickFn: func(name string) { atomic.AddInt32(&kickCalls, 1) },
	}
	resp, err := svc.TrxVoteAprRefresh(ctx, connect.NewRequest(&v1.TrxVoteAprRefreshRequest{}))
	if err != nil {
		t.Fatalf("TrxVoteAprRefresh empty table: %v", err)
	}
	if atomic.LoadInt32(&kickCalls) != 1 {
		t.Errorf("kickCalls = %d, want 1 (kick 仍应调用)", kickCalls)
	}
	if resp.Msg.CurrentRate != 0 {
		t.Errorf("current_rate = %v, want 0 (空表)", resp.Msg.CurrentRate)
	}
	if resp.Msg.FetchedAt != nil {
		t.Error("fetched_at != nil, want nil (空表)")
	}
}
