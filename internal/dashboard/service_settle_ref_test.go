// fillSettleRef 单测（D-318 T2-T5）：符号数学/双 perp 与缺省/非 perp 缺省/RPC 路径回归。
// DSN 真实夹具（D-207）；每场景独立 Service 实例（settle/pred 缓存 per-Service，防串扰）；
// 场景起始 TRUNCATE facts 自隔离（D-316① 同法）。
package dashboard

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"

	"connectrpc.com/connect"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

func settleRefSetup(t *testing.T) (*Service, *pgstore.Store) {
	t.Helper()
	ctx := context.Background()
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `TRUNCATE facts, opportunities`)
	})
	if _, err := st.Pool().Exec(ctx, `TRUNCATE facts, opportunities`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	return &Service{store: st, notionalUSD: 10000}, st
}

func settleSeedFacts(t *testing.T, st *pgstore.Store, rows []fact.Fact) {
	t.Helper()
	if err := st.InsertFacts(context.Background(), rows); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
}

func settleFacts(rows ...fact.Fact) []fact.Fact {
	now := time.Now().UTC()
	out := make([]fact.Fact, 0, len(rows))
	for i := range rows {
		rows[i].Unit = fact.UnitPctAnnualized
		rows[i].Ts = now.Add(-time.Duration(i+1) * time.Hour)
		rows[i].Src = "test"
		out = append(out, rows[i])
	}
	return out
}

// TestFillSettleRef_SignMath T2（D-318）：FUNDING_HEDGE buy=mt5:x（非 perp 腿原样保留）/
// sell=binance:perp（sign=+1），net=10、pred=−2、settleAvg=4 → settleNet=10−(+1)(−2)+(+1)(4)=16；
// USD=16/100×10000=1600（复用 netUSD 同 notional）；samples=窗内行数 2。
func TestFillSettleRef_SignMath(t *testing.T) {
	s, st := settleRefSetup(t)
	ctx := context.Background()
	settleSeedFacts(t, st, settleFacts(
		fact.Fact{Kind: "funding", Venue: "binance", Symbol: "BTCUSDT", Value: -2},
		fact.Fact{Kind: "funding_history", Venue: "binance", Symbol: "BTCUSDT", Value: 3},
		fact.Fact{Kind: "funding_history", Venue: "binance", Symbol: "BTCUSDT", Value: 5},
	))
	o := &pgstore.Opportunity{
		Type: "FUNDING_HEDGE", Canonical: "BTCUSDT",
		VenueBuy: "mt5:x", VenueSell: "binance:perp", NetBps: 10,
	}
	pb := s.oppToProto(ctx, o)
	if pb.SettleNetBps == nil || math.Abs(*pb.SettleNetBps-16) > 1e-9 {
		t.Fatalf("T2: settle_net_bps want 16, got %v", pb.SettleNetBps)
	}
	if pb.SettleNetUsd == nil || math.Abs(*pb.SettleNetUsd-1600) > 1e-9 {
		t.Fatalf("T2: settle_net_usd want 1600, got %v", pb.SettleNetUsd)
	}
	if pb.SettleSamples == nil || *pb.SettleSamples != 2 {
		t.Fatalf("T2: settle_samples want 2, got %v", pb.SettleSamples)
	}
}

// TestFillSettleRef_DualPerpAndDefault T3：双 perp 腿 sign 反向（buy −1/sell +1）数学正确；
// 任一腿缺 history → 三字段全缺省。
func TestFillSettleRef_DualPerpAndDefault(t *testing.T) {
	s, st := settleRefSetup(t)
	ctx := context.Background()
	eth := "ETHUSDT"
	settleSeedFacts(t, st, settleFacts(
		fact.Fact{Kind: "funding", Venue: "binance", Symbol: "BTCUSDT", Value: 2},
		fact.Fact{Kind: "funding", Venue: "okx", Symbol: "ETHUSDT", Value: 3},
		fact.Fact{Kind: "funding_history", Venue: "binance", Symbol: "BTCUSDT", Value: 2},
		fact.Fact{Kind: "funding_history", Venue: "okx", Symbol: "ETHUSDT", Value: 8, Ts: time.Now().UTC().Add(-30 * time.Minute)},
	))
	o := &pgstore.Opportunity{
		Type: "PERP_FUNDING", Canonical: "BTCUSDT", CanonicalSell: &eth,
		VenueBuy: "binance:perp", VenueSell: "okx:perp", NetBps: 5,
	}
	// settleNet = 5 − (−1)(2) − (+1)(3) + (−1)(2) + (+1)(8) = 10；samples=min(1,2)=1
	pb := s.oppToProto(ctx, o)
	if pb.SettleNetBps == nil || math.Abs(*pb.SettleNetBps-10) > 1e-9 {
		t.Fatalf("T3: settle_net_bps want 10, got %v", pb.SettleNetBps)
	}
	if pb.SettleSamples == nil || *pb.SettleSamples != 1 {
		t.Fatalf("T3: settle_samples want 1 (min), got %v", pb.SettleSamples)
	}

	// 缺 okx history → 整体缺省（新 Service 实例绕开缓存）
	if _, err := st.Pool().Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatalf("TRUNCATE facts: %v", err)
	}
	settleSeedFacts(t, st, settleFacts(
		fact.Fact{Kind: "funding", Venue: "binance", Symbol: "BTCUSDT", Value: 2},
		fact.Fact{Kind: "funding", Venue: "okx", Symbol: "ETHUSDT", Value: 3},
		fact.Fact{Kind: "funding_history", Venue: "binance", Symbol: "BTCUSDT", Value: 2},
	))
	s2 := &Service{store: st, notionalUSD: 10000}
	pb2 := s2.oppToProto(ctx, o)
	if pb2.SettleNetBps != nil || pb2.SettleNetUsd != nil || pb2.SettleSamples != nil {
		t.Fatalf("T3: missing history want all-default, got %v/%v/%v", pb2.SettleNetBps, pb2.SettleNetUsd, pb2.SettleSamples)
	}
}

// TestFillSettleRef_NonPerpDefaults T4：非 perp 型（SWAPARB/CROSS_VENUE/纯 MT CARRY）→ 字段缺省。
func TestFillSettleRef_NonPerpDefaults(t *testing.T) {
	s, _ := settleRefSetup(t)
	ctx := context.Background()
	for _, o := range []*pgstore.Opportunity{
		{Type: "SWAPARB", Canonical: "AUDJPY", VenueBuy: "vA:mt5", VenueSell: "vB:mt5", NetBps: 3},
		{Type: "CROSS_VENUE", Canonical: "BRENT", VenueBuy: "ic:mt5", VenueSell: "pxbt:mt5", NetBps: 300},
		{Type: "CARRY", Canonical: "TRXUSDT", VenueBuy: "binance:spot", VenueSell: "okx:spot", NetBps: 20},
	} {
		pb := s.oppToProto(ctx, o)
		if pb.SettleNetBps != nil || pb.SettleNetUsd != nil || pb.SettleSamples != nil {
			t.Fatalf("T4: type %s want all-default (无 perp 腿), got %v/%v/%v",
				o.Type, pb.SettleNetBps, pb.SettleNetUsd, pb.SettleSamples)
		}
	}
}

// TestListOpportunities_SettleFields T5：ListOpportunities RPC 返回行带 settle 字段
// （stream 路径共用 oppToProto，本锚任选 RPC 回归）。
func TestListOpportunities_SettleFields(t *testing.T) {
	s, st := settleRefSetup(t)
	ctx := context.Background()
	settleSeedFacts(t, st, settleFacts(
		fact.Fact{Kind: "funding", Venue: "binance", Symbol: "BTCUSDT", Value: -2},
		fact.Fact{Kind: "funding_history", Venue: "binance", Symbol: "BTCUSDT", Value: 3},
		fact.Fact{Kind: "funding_history", Venue: "binance", Symbol: "BTCUSDT", Value: 5},
	))
	opp := &pgstore.Opportunity{
		ID:   "FUNDING_HEDGE:BTCUSDT:mt5:x:binance:perp",
		Type: "FUNDING_HEDGE", Canonical: "BTCUSDT",
		VenueBuy: "mt5:x", VenueSell: "binance:perp", NetBps: 10,
		ExpiresAt: time.Now().UTC().Add(time.Hour), // ListOpportunities 只回未过期行（§8.2）
	}
	if _, err := st.UpsertOpportunity(ctx, opp); err != nil {
		t.Fatalf("upsert opportunity: %v", err)
	}
	resp, err := s.ListOpportunities(ctx, connect.NewRequest(&v1.ListOpportunitiesRequest{}))
	if err != nil {
		t.Fatalf("ListOpportunities: %v", err)
	}
	var got *v1.Opportunity
	for _, pb := range resp.Msg.Opportunities {
		if pb.Id == opp.ID {
			got = pb
		}
	}
	if got == nil {
		t.Fatal("T5: seeded opportunity not in RPC response")
	}
	if got.SettleNetBps == nil || math.Abs(*got.SettleNetBps-16) > 1e-9 {
		t.Fatalf("T5: RPC settle_net_bps want 16, got %v", got.SettleNetBps)
	}
}
