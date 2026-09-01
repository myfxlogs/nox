// D-182 S1 单测：实测零表达——恒零段检测 → settled=0 fact。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// TestD182_DetectZero_ConstantZero48h 恒零 48h → 写 settled=0。
func TestD182_DetectZero_ConstantZero48h(t *testing.T) {
	snaps := []pgstore.SwapSnapshot{
		{PositionID: 1, Venue: "277842155:mt5", SwapUSD: 0, Ts: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)},
		{PositionID: 1, Venue: "277842155:mt5", SwapUSD: 0, Ts: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)},
		{PositionID: 1, Venue: "277842155:mt5", SwapUSD: 0, Ts: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, // span=48h
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "277842155:mt5", Symbol: "AUDJPY", Side: "SELL", Qty: 0.01},
	}
	facts := DetectZeroSettlementFacts(snaps, legs, "277842155:mt5")
	if len(facts) != 1 {
		t.Fatalf("want 1 fact, got %d", len(facts))
	}
	if facts[0].Kind != fact.KindSwapSettledShort {
		t.Errorf("kind: want %s, got %s", fact.KindSwapSettledShort, facts[0].Kind)
	}
	if facts[0].Value != 0 {
		t.Errorf("value: want 0, got %.4f", facts[0].Value)
	}
	if facts[0].Venue != "277842155" {
		t.Errorf("venue: want 277842155, got %s", facts[0].Venue)
	}
	if facts[0].Src != "swap_snapshots" {
		t.Errorf("src: want swap_snapshots, got %s", facts[0].Src)
	}
}

// TestD182_DetectZero_ConstantZero24h 恒零 24h → 不写（跨度不足 48h）。
func TestD182_DetectZero_ConstantZero24h(t *testing.T) {
	snaps := []pgstore.SwapSnapshot{
		{PositionID: 1, Venue: "277842155:mt5", SwapUSD: 0, Ts: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{PositionID: 1, Venue: "277842155:mt5", SwapUSD: 0, Ts: time.Date(2026, 9, 1, 24, 0, 0, 0, time.UTC)}, // span=24h
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "277842155:mt5", Symbol: "AUDJPY", Side: "SELL", Qty: 0.01},
	}
	facts := DetectZeroSettlementFacts(snaps, legs, "277842155:mt5")
	if len(facts) != 0 {
		t.Fatalf("span<48h: want 0 facts, got %d", len(facts))
	}
}

// TestD182_DetectZero_JumpThenZero48h 有跳变后恒零段 48h → 写 settled=0（broker 中途停结算）。
func TestD182_DetectZero_JumpThenZero48h(t *testing.T) {
	snaps := []pgstore.SwapSnapshot{
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: 0, Ts: time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)},
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: -0.14, Ts: time.Date(2026, 8, 29, 21, 0, 0, 0, time.UTC)}, // 跳变
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: 0, Ts: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)},      // 跳变回 0
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: 0, Ts: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},       // 末段恒零 span=48h
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "vA:mt5", Symbol: "GBPJPY", Side: "BUY", Qty: 0.01},
	}
	facts := DetectZeroSettlementFacts(snaps, legs, "vA:mt5")
	if len(facts) != 1 {
		t.Fatalf("jump then zero 48h: want 1 fact, got %d", len(facts))
	}
	if facts[0].Value != 0 {
		t.Errorf("value: want 0, got %.4f", facts[0].Value)
	}
	if facts[0].Kind != fact.KindSwapSettledLong {
		t.Errorf("kind: want %s (BUY), got %s", fact.KindSwapSettledLong, facts[0].Kind)
	}
}

// TestD182_DetectZero_JumpThenNonZero 有跳变后非零段 → 不写（有结算行为，末段非恒零）。
func TestD182_DetectZero_JumpThenNonZero(t *testing.T) {
	snaps := []pgstore.SwapSnapshot{
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: 0, Ts: time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)},
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: -0.14, Ts: time.Date(2026, 8, 29, 21, 0, 0, 0, time.UTC)}, // 跳变
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: -0.14, Ts: time.Date(2026, 8, 31, 21, 0, 0, 0, time.UTC)}, // 末段非零 span=48h
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "vA:mt5", Symbol: "GBPJPY", Side: "SELL", Qty: 0.01},
	}
	facts := DetectZeroSettlementFacts(snaps, legs, "vA:mt5")
	if len(facts) != 0 {
		t.Fatalf("non-zero tail: want 0 facts, got %d", len(facts))
	}
}

// TestD182_DetectZero_SingleSnapshot 单条快照 → 不写（跨度 0）。
func TestD182_DetectZero_SingleSnapshot(t *testing.T) {
	snaps := []pgstore.SwapSnapshot{
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: 0, Ts: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "vA:mt5", Symbol: "AUDJPY", Side: "SELL", Qty: 0.01},
	}
	facts := DetectZeroSettlementFacts(snaps, legs, "vA:mt5")
	if len(facts) != 0 {
		t.Fatalf("single snapshot: want 0 facts, got %d", len(facts))
	}
}

// TestD182_DetectZero_NoOpenLeg 无 OPEN 腿 → 不写。
func TestD182_DetectZero_NoOpenLeg(t *testing.T) {
	snaps := []pgstore.SwapSnapshot{
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: 0, Ts: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)},
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: 0, Ts: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	}
	legs := []pgstore.PositionLeg{
		{Phase: "CLOSED", Venue: "vA:mt5", Symbol: "AUDJPY", Side: "SELL", Qty: 0.01},
	}
	facts := DetectZeroSettlementFacts(snaps, legs, "vA:mt5")
	if len(facts) != 0 {
		t.Fatalf("no open leg: want 0 facts, got %d", len(facts))
	}
}

// TestD182_RecordZeroForPosition_Idempotent scanAndClose 路径幂等——同 posID 两次调用只写一条。
func TestD182_RecordZeroForPosition_Idempotent(t *testing.T) {
	store := &fakeSettlementProfileStore{
		legsByPos: map[int64][]pgstore.PositionLeg{
			1: {{Phase: "OPEN", Venue: "277842155:mt5", Symbol: "AUDJPY", Side: "SELL", Qty: 0.01}},
		},
		snaps: []pgstore.SwapSnapshot{
			{PositionID: 1, Venue: "277842155:mt5", SwapUSD: 0, Ts: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)},
			{PositionID: 1, Venue: "277842155:mt5", SwapUSD: 0, Ts: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, // span=48h
		},
	}
	c := NewSettlementProfileCollector(store)
	c.RecordZeroForPosition(context.Background(), 1)
	if len(store.facts) != 1 {
		t.Fatalf("first call: want 1 fact, got %d", len(store.facts))
	}
	if store.facts[0].Value != 0 {
		t.Errorf("value: want 0, got %.4f", store.facts[0].Value)
	}
	// 第二次同 posID → 幂等跳过
	c.RecordZeroForPosition(context.Background(), 1)
	if len(store.facts) != 1 {
		t.Fatalf("second call: want 1 fact (idempotent), got %d", len(store.facts))
	}
}

// TestD182_BackfillZeroDetection boot 回溯路径——恒零快照 → settled=0 fact。
func TestD182_BackfillZeroDetection(t *testing.T) {
	store := &fakeSettlementProfileStore{
		positions: []pgstore.Position{{ID: 1, Status: "OPEN"}},
		legsByPos: map[int64][]pgstore.PositionLeg{
			1: {{Phase: "OPEN", Venue: "277842155:mt5", Symbol: "AUDJPY", Side: "SELL", Qty: 0.01}},
		},
		snaps: []pgstore.SwapSnapshot{
			{PositionID: 1, Venue: "277842155:mt5", SwapUSD: 0, Ts: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)},
			{PositionID: 1, Venue: "277842155:mt5", SwapUSD: 0, Ts: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, // span=48h, 恒零
		},
	}
	c := NewSettlementProfileCollector(store)
	if err := c.BackfillFromHistory(context.Background()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	// 无跳变 → 0 jump facts，但有 1 zero fact
	zeroFacts := 0
	for _, f := range store.facts {
		if f.Value == 0 && f.Kind == fact.KindSwapSettledShort {
			zeroFacts++
		}
	}
	if zeroFacts != 1 {
		t.Fatalf("want 1 zero fact, got %d (total facts=%d)", zeroFacts, len(store.facts))
	}
}
