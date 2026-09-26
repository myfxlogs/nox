// D-231 S3 测试：卡片投票收益率三字段透传（SSOT §验收标准 2④）。
// fin_trx_vote_apr 有数据 → HedgeLegSpot.vote_apr_current/max/spread/max_sr/ts 透传。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"connectrpc.com/connect"

	v1 "arb/internal/gen/dashboardv1"
)

// TestGetHedgeStructure_VoteAprPassthrough 验证 ④卡片三字段透传：
// fin_trx_vote_apr 有数据 → HedgeLegSpot.vote_apr_current/max/spread 透传。
func TestGetHedgeStructure_VoteAprPassthrough(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// 写一条 TRX 账户（leg_spot 非 nil 需要）
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", VotesTotal: 18070,
		BalanceSun: 97863827, AllowanceSun: 7717994,
		EnergyDelegated: 17270000000, BandwidthStaked: 800000000,
		FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	// 写一条 fin_trx_vote_apr（卡片透传源）
	maxSr := "SR-A"
	if err := s.InsertTRXVoteApr(ctx, pgstore.TRXVoteApr{
		FetchedAt: now, CurrentRate: 2.90, CurrentSr: &maxSr,
		MaxRate: 3.10, MaxSr: &maxSr, Spread: 0.20,
	}); err != nil {
		t.Fatalf("InsertTRXVoteApr: %v", err)
	}

	svc := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("GetHedgeStructure: %v", err)
	}
	leg := resp.Msg.Structure.LegSpot
	if leg == nil {
		t.Fatal("leg_spot = nil")
	}
	// 三字段透传
	if leg.VoteAprCurrent != 2.90 {
		t.Errorf("vote_apr_current = %v, want 2.90", leg.VoteAprCurrent)
	}
	if leg.VoteAprMax != 3.10 {
		t.Errorf("vote_apr_max = %v, want 3.10", leg.VoteAprMax)
	}
	if leg.VoteAprSpread != 0.20 {
		t.Errorf("vote_apr_spread = %v, want 0.20", leg.VoteAprSpread)
	}
	if leg.VoteAprMaxSr != "SR-A" {
		t.Errorf("vote_apr_max_sr = %v, want SR-A", leg.VoteAprMaxSr)
	}
	if leg.VoteAprTs == nil {
		t.Error("vote_apr_ts = nil, want timestamp")
	}
}

// TestGetHedgeStructure_VoteAprEmpty 验证空表容忍：
// fin_trx_vote_apr 空表 → vote_apr_* 零值，不 panic。
func TestGetHedgeStructure_VoteAprEmpty(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// 只写 TRX 账户（无 vote_apr 数据）
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", VotesTotal: 18070,
		BalanceSun: 97863827, AllowanceSun: 7717994,
		EnergyDelegated: 17270000000, BandwidthStaked: 800000000,
		FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}

	svc := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("GetHedgeStructure: %v", err)
	}
	leg := resp.Msg.Structure.LegSpot
	if leg == nil {
		t.Fatal("leg_spot = nil")
	}
	// 空表 → 零值，不 panic
	if leg.VoteAprCurrent != 0 {
		t.Errorf("vote_apr_current = %v, want 0 (空表)", leg.VoteAprCurrent)
	}
	if leg.VoteAprMax != 0 {
		t.Errorf("vote_apr_max = %v, want 0 (空表)", leg.VoteAprMax)
	}
	if leg.VoteAprTs != nil {
		t.Error("vote_apr_ts != nil, want nil (空表)")
	}
}
