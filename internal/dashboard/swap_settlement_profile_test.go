// D-180 S1 单测：结算画像采集——跳变检测/三倍日剔除/多仓聚合中位数/换算/幂等/存量回溯。
package dashboard

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

// absDiff 返回两浮点数差的绝对值（测试比较用）。
func absDiff(a, b float64) float64 {
	if a < b {
		return b - a
	}
	return a - b
}

// fakeSettlementProfileStore 受控 SettlementProfileStore（内存模拟）。
type fakeSettlementProfileStore struct {
	positions []pgstore.Position
	legsByPos map[int64][]pgstore.PositionLeg
	facts     []fact.Fact
	snaps     []pgstore.SwapSnapshot
}

func (f *fakeSettlementProfileStore) ListPositions(_ context.Context, status string, _, _ int) ([]pgstore.Position, error) {
	var out []pgstore.Position
	for _, p := range f.positions {
		if p.Status == status {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fakeSettlementProfileStore) GetPosition(_ context.Context, id int64) (*pgstore.Position, error) {
	for i := range f.positions {
		if f.positions[i].ID == id {
			return &f.positions[i], nil
		}
	}
	return nil, fmt.Errorf("position %d not found", id)
}
func (f *fakeSettlementProfileStore) ListPositionLegs(_ context.Context, posID int64) ([]pgstore.PositionLeg, error) {
	return f.legsByPos[posID], nil
}
func (f *fakeSettlementProfileStore) LatestFacts(_ context.Context, kind, venue, symbol string) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, ft := range f.facts {
		if (kind == "" || ft.Kind == kind) && (venue == "" || ft.Venue == venue) && (symbol == "" || ft.Symbol == symbol) {
			out = append(out, ft)
		}
	}
	return out, nil
}
func (f *fakeSettlementProfileStore) InsertFacts(_ context.Context, facts []fact.Fact) error {
	f.facts = append(f.facts, facts...)
	return nil
}
func (f *fakeSettlementProfileStore) FactExistsAt(_ context.Context, kind, venue, symbol string, ts time.Time) (bool, error) {
	for _, ft := range f.facts {
		if ft.Kind == kind && ft.Venue == venue && ft.Symbol == symbol && ft.Ts.Equal(ts) {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeSettlementProfileStore) ListAllSwapSnapshots(_ context.Context) ([]pgstore.SwapSnapshot, error) {
	return f.snaps, nil
}
func (f *fakeSettlementProfileStore) ListSwapSnapshotsByPosID(_ context.Context, posID int64) ([]pgstore.SwapSnapshot, error) {
	var out []pgstore.SwapSnapshot
	for _, s := range f.snaps {
		if s.PositionID == posID {
			out = append(out, s)
		}
	}
	return out, nil
}

// TestD180_DetectJumps_PositiveJump 0→+0.05 跳变检测（BUY 腿，usdPerLot=0.05/0.01=5.0）。
func TestD180_DetectJumps_PositiveJump(t *testing.T) {
	snaps := []pgstore.SwapSnapshot{
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: 0, Ts: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: 0.05, Ts: time.Date(2026, 9, 1, 21, 0, 0, 0, time.UTC)},
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "vA:mt5", Symbol: "GBPJPY", Side: "BUY", Qty: 0.01},
	}
	jumps := detectJumpsFromSnapshots(snaps, legs, "vA:mt5")
	if len(jumps) != 1 {
		t.Fatalf("want 1 jump, got %d", len(jumps))
	}
	if jumps[0].side != "BUY" {
		t.Errorf("side: want BUY, got %s", jumps[0].side)
	}
	if jumps[0].usdPerLot != 5.0 {
		t.Errorf("usdPerLot: want 5.0 (0.05/0.01), got %.4f", jumps[0].usdPerLot)
	}
	if jumps[0].settlementDay != "2026-09-01" {
		t.Errorf("settlementDay: want 2026-09-01, got %s", jumps[0].settlementDay)
	}
}

// TestD180_DetectJumps_NegativeJump 0→−0.14 跳变检测（SELL 腿，负向脱钩实证）。
func TestD180_DetectJumps_NegativeJump(t *testing.T) {
	snaps := []pgstore.SwapSnapshot{
		{PositionID: 1, Venue: "61585565:mt5", SwapUSD: 0, Ts: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{PositionID: 1, Venue: "61585565:mt5", SwapUSD: -0.14, Ts: time.Date(2026, 9, 1, 21, 0, 0, 0, time.UTC)},
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "61585565:mt5", Symbol: "GBPJPY", Side: "SELL", Qty: 0.01},
	}
	jumps := detectJumpsFromSnapshots(snaps, legs, "61585565:mt5")
	if len(jumps) != 1 {
		t.Fatalf("want 1 jump, got %d", len(jumps))
	}
	if jumps[0].side != "SELL" {
		t.Errorf("side: want SELL, got %s", jumps[0].side)
	}
	if absDiff(jumps[0].usdPerLot, -14.0) > 1e-9 {
		t.Errorf("usdPerLot: want -14.0 (-0.14/0.01), got %.4f", jumps[0].usdPerLot)
	}
	if jumps[0].venue != "61585565" {
		t.Errorf("venue: want 61585565 (bare), got %s", jumps[0].venue)
	}
}

// TestD180_DetectJumps_NoChangeSkip 无变化跳过。
func TestD180_DetectJumps_NoChangeSkip(t *testing.T) {
	snaps := []pgstore.SwapSnapshot{
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: 5.0, Ts: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: 5.0, Ts: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)},
		{PositionID: 1, Venue: "vA:mt5", SwapUSD: 5.0, Ts: time.Date(2026, 9, 1, 21, 0, 0, 0, time.UTC)},
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "vA:mt5", Symbol: "AUDJPY", Side: "BUY", Qty: 0.01},
	}
	jumps := detectJumpsFromSnapshots(snaps, legs, "vA:mt5")
	if len(jumps) != 0 {
		t.Fatalf("no change: want 0 jumps, got %d", len(jumps))
	}
}

// TestD180_TripleSwapDaySkip 三倍日剔除——triple_swap_day fact 命中 → skip。
func TestD180_TripleSwapDaySkip(t *testing.T) {
	store := &fakeSettlementProfileStore{
		facts: []fact.Fact{
			{Kind: fact.KindTripleSwapDay, Venue: "vA", Symbol: "GBPJPY", Value: float64(time.Wednesday), Unit: fact.UnitWeekday},
		},
	}
	c := NewSettlementProfileCollector(store)
	// 跳变 ts 是周三 → 应被剔除
	jumps := []swapJump{
		{venue: "vA", symbol: "GBPJPY", side: "BUY", settlementDay: "2026-09-02", usdPerLot: 5.0, ts: time.Date(2026, 9, 2, 21, 0, 0, 0, time.UTC)},
	}
	filtered := c.filterTripleSwapDays(context.Background(), jumps)
	if len(filtered) != 0 {
		t.Fatalf("triple swap day: want 0 filtered, got %d", len(filtered))
	}
	// 跳变 ts 是周四 → 不应被剔除
	jumps[0].ts = time.Date(2026, 9, 3, 21, 0, 0, 0, time.UTC)
	jumps[0].settlementDay = "2026-09-03"
	filtered = c.filterTripleSwapDays(context.Background(), jumps)
	if len(filtered) != 1 {
		t.Fatalf("non-triple day: want 1 filtered, got %d", len(filtered))
	}
}

// TestD180_MultiPositionAggregateMedian 多仓聚合中位数（471/472 双仓同日跳变归一）。
func TestD180_MultiPositionAggregateMedian(t *testing.T) {
	jumps := []swapJump{
		{venue: "61585565", symbol: "GBPJPY", side: "SELL", settlementDay: "2026-09-01", usdPerLot: -14.0, ts: time.Date(2026, 9, 1, 21, 0, 0, 0, time.UTC)},
		{venue: "61585565", symbol: "GBPJPY", side: "SELL", settlementDay: "2026-09-01", usdPerLot: -14.0, ts: time.Date(2026, 9, 1, 21, 1, 0, 0, time.UTC)},
	}
	facts := aggregateJumpsToFacts(jumps)
	if len(facts) != 1 {
		t.Fatalf("want 1 fact (aggregated), got %d", len(facts))
	}
	if facts[0].Kind != fact.KindSwapSettledShort {
		t.Errorf("kind: want %s, got %s", fact.KindSwapSettledShort, facts[0].Kind)
	}
	if absDiff(facts[0].Value, -14.0) > 1e-9 {
		t.Errorf("median: want -14.0, got %.4f", facts[0].Value)
	}
	if facts[0].Unit != fact.UnitUsdLotDay {
		t.Errorf("unit: want %s, got %s", fact.UnitUsdLotDay, facts[0].Unit)
	}
	if facts[0].Src != "swap_snapshots" {
		t.Errorf("src: want swap_snapshots, got %s", facts[0].Src)
	}
}

// TestD180_MedianOddEven 中位数函数（奇/偶数样本）。
func TestD180_MedianOddEven(t *testing.T) {
	if m := median([]float64{5.0}); m != 5.0 {
		t.Errorf("single: want 5.0, got %.4f", m)
	}
	if m := median([]float64{1.0, 3.0, 5.0}); m != 3.0 {
		t.Errorf("odd: want 3.0, got %.4f", m)
	}
	if m := median([]float64{1.0, 2.0, 3.0, 4.0}); m != 2.5 {
		t.Errorf("even: want 2.5, got %.4f", m)
	}
	if m := median([]float64{}); m != 0 {
		t.Errorf("empty: want 0, got %.4f", m)
	}
}

// TestD180_IdempotentWrite 幂等写——同 key 第二次跳过。
func TestD180_IdempotentWrite(t *testing.T) {
	store := &fakeSettlementProfileStore{}
	c := NewSettlementProfileCollector(store)
	jumps := []swapJump{
		{venue: "vA", symbol: "GBPJPY", side: "BUY", settlementDay: "2026-09-01", usdPerLot: 5.0, ts: time.Date(2026, 9, 1, 21, 0, 0, 0, time.UTC)},
	}
	c.RecordJumps(context.Background(), jumps)
	if len(store.facts) != 1 {
		t.Fatalf("first write: want 1 fact, got %d", len(store.facts))
	}
	// 第二次同 key → 幂等跳过
	c.RecordJumps(context.Background(), jumps)
	if len(store.facts) != 1 {
		t.Fatalf("second write: want 1 fact (idempotent), got %d", len(store.facts))
	}
}

// TestD180_BackfillFromHistory 存量回溯——全历史跳变 → facts。
func TestD180_BackfillFromHistory(t *testing.T) {
	store := &fakeSettlementProfileStore{
		positions: []pgstore.Position{{ID: 1, Status: "OPEN"}},
		legsByPos: map[int64][]pgstore.PositionLeg{
			1: {{Phase: "OPEN", Venue: "61585565:mt5", Symbol: "GBPJPY", Side: "SELL", Qty: 0.01}},
		},
		snaps: []pgstore.SwapSnapshot{
			{PositionID: 1, Venue: "61585565:mt5", SwapUSD: 0, Ts: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)},
			{PositionID: 1, Venue: "61585565:mt5", SwapUSD: -0.14, Ts: time.Date(2026, 8, 31, 21, 0, 0, 0, time.UTC)},
			{PositionID: 1, Venue: "61585565:mt5", SwapUSD: -0.28, Ts: time.Date(2026, 9, 1, 21, 0, 0, 0, time.UTC)},
		},
	}
	c := NewSettlementProfileCollector(store)
	if err := c.BackfillFromHistory(context.Background()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	// 2 跳变（8-31 和 9-1），同 venue+symbol+side+不同日 → 2 facts
	if len(store.facts) != 2 {
		t.Fatalf("want 2 facts, got %d", len(store.facts))
	}
	sort.Slice(store.facts, func(i, j int) bool { return store.facts[i].Ts.Before(store.facts[j].Ts) })
	if absDiff(store.facts[0].Value, -14.0) > 1e-9 {
		t.Errorf("fact[0] value: want -14.0, got %.4f", store.facts[0].Value)
	}
	if absDiff(store.facts[1].Value, -14.0) > 1e-9 {
		t.Errorf("fact[1] value: want -14.0, got %.4f", store.facts[1].Value)
	}
	// 再次 backfill → 幂等不重复
	if err := c.BackfillFromHistory(context.Background()); err != nil {
		t.Fatalf("backfill 2nd: %v", err)
	}
	if len(store.facts) != 2 {
		t.Fatalf("2nd backfill: want 2 facts (idempotent), got %d", len(store.facts))
	}
}

// TestD180_RealtimeJumpDetection 实时链路 detectRealtimeJump。
func TestD180_RealtimeJumpDetection(t *testing.T) {
	store := &fakeSettlementProfileStore{
		legsByPos: map[int64][]pgstore.PositionLeg{
			100: {{Phase: "OPEN", Venue: "61585565:mt5", Symbol: "GBPJPY", Side: "SELL", Qty: 0.01}},
		},
	}
	o := reconcile.VenueOrder{VenueKey: "61585565:mt5", Swap: -0.14}
	now := time.Date(2026, 9, 1, 21, 0, 0, 0, time.UTC)
	j, ok := detectRealtimeJump(store, 100, o, 0.0, now)
	if !ok {
		t.Fatal("detectRealtimeJump: expected ok=true")
	}
	if absDiff(j.usdPerLot, -14.0) > 1e-9 {
		t.Errorf("usdPerLot: want -14.0, got %.4f", j.usdPerLot)
	}
	if j.side != "SELL" {
		t.Errorf("side: want SELL, got %s", j.side)
	}
	if j.venue != "61585565" {
		t.Errorf("venue: want 61585565, got %s", j.venue)
	}
}

// TestD180_RealtimeJumpNoPrev 首次写入无前值不算跳变（hasPrev=false 路径）。
func TestD180_RealtimeJumpNoPrev(t *testing.T) {
	store := &fakeSettlementProfileStore{
		legsByPos: map[int64][]pgstore.PositionLeg{
			100: {{Phase: "OPEN", Venue: "vA:mt5", Symbol: "AUDJPY", Side: "BUY", Qty: 0.01}},
		},
	}
	o := reconcile.VenueOrder{VenueKey: "vA:mt5", Swap: 5.0}
	// prev=5.0 same as current → jumpUSD=0 → ok=false
	_, ok := detectRealtimeJump(store, 100, o, 5.0, time.Now().UTC())
	if ok {
		t.Fatal("same swap: expected ok=false (no jump)")
	}
}
