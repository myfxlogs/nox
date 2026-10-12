// D-254 测试：TrxAccountRefresh handler——双踢 trx_account+trx_apr + 返回最新 fin_trx_account 全字段快照。
// 先红后绿：handler 未实现前编译失败（v1.TrxAccountRefreshRequest 不存在）；实现后验证双踢+字段透传+空表容忍+nil kickFn 安全。
package dashboard

import (
	"context"
	"encoding/json"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/store/pgstore"

	"connectrpc.com/connect"

	v1 "arb/internal/gen/dashboardv1"
)

// TestD254_TrxAccountRefresh_DoubleKickAndReturn 验证 handler 双踢 trx_account+trx_apr 后返回最新 fin_trx_account 全字段。
func TestD254_TrxAccountRefresh_DoubleKickAndReturn(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	srAddr := "SR-TronSpark"
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address:         "TRX-ADDR-1",
		VotesTotal:      18070,
		SRAddress:       &srAddr,
		BalanceSun:      50_000_000, // 50 TRX
		AllowanceSun:    1_500_000,  // 1.5 TRX（链上 allowance，已不用作待领取展示）
		RewardNumSun:    1_500_000,  // 1.5 TRX（D-285 链上真值，待领取展示源）
		EnergyDelegated: 8_000_000,  // 8 TRX
		BandwidthStaked: 2_000_000,  // 2 TRX
		NetLimit:        600_000,    // 600000 BP（链上 sun 原值）
		Unfrozen:        []byte(`[{"amount":1000000,"expires":1700000000}]`),
		FetchedAt:       now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	var kickCalls int32
	var kickedNames []string
	svc := &Service{
		store: s,
		kickFn: func(name string) {
			atomic.AddInt32(&kickCalls, 1)
			kickedNames = append(kickedNames, name)
		},
	}
	resp, err := svc.TrxAccountRefresh(ctx, connect.NewRequest(&v1.TrxAccountRefreshRequest{}))
	if err != nil {
		t.Fatalf("TrxAccountRefresh: %v", err)
	}
	// 双踢：恰 2 次调用，先 trx_account 后 trx_apr
	if atomic.LoadInt32(&kickCalls) != 2 {
		t.Errorf("kickCalls = %d, want 2 (trx_account+trx_apr)", kickCalls)
	}
	if len(kickedNames) != 2 || kickedNames[0] != "trx_account" || kickedNames[1] != "trx_apr" {
		t.Errorf("kickedNames = %v, want [trx_account, trx_apr]", kickedNames)
	}
	// 全 11 字段透传（Q1 裁定）
	if resp.Msg.Address != "TRX-ADDR-1" {
		t.Errorf("address = %q, want TRX-ADDR-1", resp.Msg.Address)
	}
	if resp.Msg.SrAddress != srAddr {
		t.Errorf("sr_address = %q, want %s", resp.Msg.SrAddress, srAddr)
	}
	if resp.Msg.TrxBalance != 50.0 {
		t.Errorf("trx_balance = %v, want 50.0 (50e6 sun ÷ 1e6)", resp.Msg.TrxBalance)
	}
	if resp.Msg.TrxAllowance != 1.5 {
		t.Errorf("trx_allowance = %v, want 1.5 (reward_num_sun 1.5e6 ÷ 1e6, D-285)", resp.Msg.TrxAllowance)
	}
	if resp.Msg.EnergyDelegated != 8.0 {
		t.Errorf("energy_delegated = %v, want 8.0", resp.Msg.EnergyDelegated)
	}
	if resp.Msg.BandwidthStaked != 2.0 {
		t.Errorf("bandwidth_staked = %v, want 2.0", resp.Msg.BandwidthStaked)
	}
	if resp.Msg.NetLimit != 600000 {
		t.Errorf("net_limit = %v, want 600000 (BP 原值不 ÷1e6)", resp.Msg.NetLimit)
	}
	// D-278 返修：资源字段不 ÷1e6，BP/Energy 原值透传（种子未设 net_used 等默认 0）
	if resp.Msg.NetUsed != 0 {
		t.Errorf("net_used = %v, want 0 (种子未设)", resp.Msg.NetUsed)
	}
	if resp.Msg.FreeNetLimit != 0 {
		t.Errorf("free_net_limit = %v, want 0 (种子未设)", resp.Msg.FreeNetLimit)
	}
	if resp.Msg.EnergyLimit != 0 {
		t.Errorf("energy_limit = %v, want 0 (种子未设)", resp.Msg.EnergyLimit)
	}
	if resp.Msg.Votes != 18070 {
		t.Errorf("votes = %v, want 18070 (votes_total 不除 1e6)", resp.Msg.Votes)
	}
	// R-J2：unfrozen 语义比较——jsonb 规范化文本输出（分隔符后带空格）vs 种子紧凑形态，逐字比较必 FAIL。
	// handler 透传行为正确（禁改 handler），测试侧两侧 unmarshal 为 []map[string]any 后 DeepEqual。
	{
		want := `[{"amount":1000000,"expires":1700000000}]`
		var gotVal, wantVal []map[string]any
		if err := json.Unmarshal([]byte(resp.Msg.Unfrozen), &gotVal); err != nil {
			t.Fatalf("unfrozen got unmarshal: %v", err)
		}
		if err := json.Unmarshal([]byte(want), &wantVal); err != nil {
			t.Fatalf("unfrozen want unmarshal: %v", err)
		}
		if !reflect.DeepEqual(gotVal, wantVal) {
			t.Errorf("unfrozen = %q, want JSONB 语义等价 %q", resp.Msg.Unfrozen, want)
		}
	}
	if resp.Msg.FetchedAt == nil {
		t.Error("fetched_at = nil, want timestamp")
	}
}

// TestD254_TrxAccountRefresh_NilKickFn 验证 kickFn nil 时不 panic（测试/未接线场景）。
func TestD254_TrxAccountRefresh_NilKickFn(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address:    "TRX-ADDR-2",
		BalanceSun: 10_000_000,
		FetchedAt:  now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	svc := &Service{store: s} // kickFn nil
	resp, err := svc.TrxAccountRefresh(ctx, connect.NewRequest(&v1.TrxAccountRefreshRequest{}))
	if err != nil {
		t.Fatalf("TrxAccountRefresh nil kickFn: %v", err)
	}
	if resp.Msg.TrxBalance != 10.0 {
		t.Errorf("trx_balance = %v, want 10.0", resp.Msg.TrxBalance)
	}
}

// TestD254_TrxAccountRefresh_EmptyTable 验证空表返回零值响应（不报错）。
func TestD254_TrxAccountRefresh_EmptyTable(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	var kickCalls int32
	svc := &Service{
		store:  s,
		kickFn: func(name string) { atomic.AddInt32(&kickCalls, 1) },
	}
	resp, err := svc.TrxAccountRefresh(ctx, connect.NewRequest(&v1.TrxAccountRefreshRequest{}))
	if err != nil {
		t.Fatalf("TrxAccountRefresh empty table: %v", err)
	}
	// 空表仍应双踢（kick 是触发采集，与表是否有数据无关）
	if atomic.LoadInt32(&kickCalls) != 2 {
		t.Errorf("kickCalls = %d, want 2 (空表仍双踢)", kickCalls)
	}
	if resp.Msg.TrxBalance != 0 {
		t.Errorf("trx_balance = %v, want 0 (空表)", resp.Msg.TrxBalance)
	}
	if resp.Msg.FetchedAt != nil {
		t.Error("fetched_at != nil, want nil (空表)")
	}
}

// TestD254_TrxAccountRefresh_NilFields 验证 nil 字段（sr_address/unfrozen）安全透传为零值。
func TestD254_TrxAccountRefresh_NilFields(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address:   "TRX-ADDR-3",
		SRAddress: nil, // 无投票
		Unfrozen:  nil, // 无在途解押
		FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	svc := &Service{store: s}
	resp, err := svc.TrxAccountRefresh(ctx, connect.NewRequest(&v1.TrxAccountRefreshRequest{}))
	if err != nil {
		t.Fatalf("TrxAccountRefresh nil fields: %v", err)
	}
	if resp.Msg.SrAddress != "" {
		t.Errorf("sr_address = %q, want empty (nil→空字符串)", resp.Msg.SrAddress)
	}
	if resp.Msg.Unfrozen != "" {
		t.Errorf("unfrozen = %q, want empty (nil→空字符串)", resp.Msg.Unfrozen)
	}
}
