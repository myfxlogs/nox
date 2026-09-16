package swapledger

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// TestAccountPosition_XAssetCarryPerLegKind D-314 测试锚 9：
// XASSET_CARRY 混合 venue 持仓——gate:perp 腿写 kind=funding cashflow，mt5 腿写 kind=swap cashflow；
// per-leg 幂等查询同 kind 路由（hasQueries 断言 "0:funding"/"1:swap"）。
func TestAccountPosition_XAssetCarryPerLegKind(t *testing.T) {
	fundMap := map[factKey]fact.Fact{factKey{"CLUSDT", "gate"}: {Value: 10.95, Ts: time.Now()}} // 10.95% 年化
	longMap := map[factKey]fact.Fact{factKey{"CLUSDT", "277842155"}: {Value: 5.0, Ts: time.Now()}}
	notional := stubNotional{vals: map[string]float64{
		"CLUSDT:gate:perp:bid":     100000, // leg0 SELL→bid
		"CLUSDT:277842155:mt5:ask": 100000, // leg1 BUY→ask
	}}
	r := stubTripleDay{venueDays: map[string]time.Weekday{"277842155": time.Wednesday}}
	store := &nonSettleStubStore{
		hasCashflowLegs: map[int16]bool{},
		legs: []pgstore.PositionLeg{
			{LegNo: 0, Phase: "OPEN", Venue: "gate:perp", Side: "SELL", Qty: 1.0},
			{LegNo: 1, Phase: "OPEN", Venue: "277842155:mt5", Side: "BUY", Qty: 1.0},
		},
	}
	l := &Ledger{store: store, notional: notional, tripleDay: r}

	pos := pgstore.Position{ID: 484, Type: "XASSET_CARRY", OpportunityID: "XASSET_CARRY:CLUSDT:277842155:mt5:gate:perp:1"}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	n, err := l.accountPosition(context.Background(), pos, monday, longMap, map[factKey]fact.Fact{}, fundMap)
	if err != nil {
		t.Fatalf("accountPosition: %v", err)
	}
	if n != 2 {
		t.Fatalf("written=%d, want 2 (perp 腿 funding + mt5 腿 swap)", n)
	}
	if len(store.insertedCFs) != 2 {
		t.Fatalf("insertedCFs len=%d, want 2", len(store.insertedCFs))
	}
	if got := store.insertedCFs[0].Kind; got != "funding" {
		t.Errorf("leg0 kind=%q, want funding (gate:perp)", got)
	}
	if got := store.insertedCFs[1].Kind; got != "swap" {
		t.Errorf("leg1 kind=%q, want swap (277842155:mt5)", got)
	}
	// per-leg 幂等查询按各自 kind 路由（互不干扰）
	wantQueries := []string{"0:funding", "1:swap"}
	if len(store.hasQueries) != 2 || store.hasQueries[0] != wantQueries[0] || store.hasQueries[1] != wantQueries[1] {
		t.Errorf("hasQueries=%v, want %v", store.hasQueries, wantQueries)
	}
	// event_key 带 per-leg kind
	if k := store.insertedCFs[0].EventKey; k == nil || *k != "swapledger:484:0:funding:2026-08-24" {
		t.Errorf("leg0 event_key=%v, want swapledger:484:0:funding:2026-08-24", k)
	}
	if k := store.insertedCFs[1].EventKey; k == nil || *k != "swapledger:484:1:swap:2026-08-24" {
		t.Errorf("leg1 event_key=%v, want swapledger:484:1:swap:2026-08-24", k)
	}
}
