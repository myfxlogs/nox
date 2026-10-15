// earn_yield_test.go —— D-524 锚：池行增量归属入账序列对拍。
package dashboard

import (
	"context"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

type fakeYieldStore struct {
	prev map[string]struct {
		eq  float64
		ts  time.Time
		has bool
	}
	flows  float64
	shares map[string]float64
	events []pgstore.IncomeEvent
}

func (f *fakeYieldStore) PrevSnapshotEquity(_ context.Context, _, _, ccy string, _ time.Time) (float64, time.Time, bool, error) {
	p := f.prev[ccy]
	return p.eq, p.ts, p.has, nil
}
func (f *fakeYieldStore) EarnSubscribeSum(context.Context, string, time.Time, time.Time) (float64, error) {
	return f.flows, nil
}
func (f *fakeYieldStore) InsertIncomeEvents(_ context.Context, evs []pgstore.IncomeEvent) (int64, error) {
	f.events = append(f.events, evs...)
	return int64(len(evs)), nil
}
func (f *fakeYieldStore) ExchangeConfigID(context.Context, string, string) (int64, error) {
	return 42, nil
}
func (f *fakeYieldStore) OwnershipShares(context.Context) (map[string]float64, error) {
	return f.shares, nil
}

func TestEarnYieldAttribution(t *testing.T) {
	fs := &fakeYieldStore{prev: map[string]struct {
		eq  float64
		ts  time.Time
		has bool
	}{}}
	m := &AccountMonitor{yieldStore: fs}
	ctx := context.Background()
	now := time.Now()

	// ①首拍基线化：prev 无行 → 零入账
	m.attributeEarnYield(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "gate", VenueType: "earn", Currency: "USDT", EquityRaw: 1999.64, FetchedAt: now},
	})
	if len(fs.events) != 0 {
		t.Fatalf("首拍误入账: %+v", fs.events)
	}

	// ②申购流入+x 后的池行：Δ=x 全为流入 → 零收益行
	fs.prev["USDT"] = struct {
		eq  float64
		ts  time.Time
		has bool
	}{1999.64, now.Add(-time.Hour), true}
	fs.flows = 500 // 同拍内本系统申购 +500（Δ 全冲销）
	m.attributeEarnYield(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "gate", VenueType: "earn", Currency: "USDT", EquityRaw: 2499.64, FetchedAt: now.Add(time.Minute)},
	})
	if len(fs.events) != 0 {
		t.Fatalf("申购流入误归收益: %+v", fs.events)
	}

	// ③复投增量：池 +0.0434 无申购流 → earn_yield=+0.0434
	fs.flows = 0
	fs.prev["USDT"] = struct {
		eq  float64
		ts  time.Time
		has bool
	}{2499.64, now.Add(-time.Minute), true}
	m.attributeEarnYield(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "gate", VenueType: "earn", Currency: "USDT", EquityRaw: 2499.6834, FetchedAt: now.Add(2 * time.Minute)},
	})
	if len(fs.events) != 1 {
		t.Fatalf("复投增量未入账: %+v", fs.events)
	}
	ev := fs.events[0]
	if ev.IncomeType != "earn_yield" || ev.Venue != "gate" || ev.VenueType != "earn" || ev.Symbol != "USDT" ||
		ev.ConfigID != 42 || ev.AmountUSD < 0.0433 || ev.AmountUSD > 0.0435 {
		t.Fatalf("收益行口径错: %+v", ev)
	}

	// ④赎回 −z：无台账流出记录 → Δ 含 −z=如实负收益（票面③：流出无账不扣）
	fs.prev["USDT"] = struct {
		eq  float64
		ts  time.Time
		has bool
	}{2499.6834, now.Add(-time.Minute), true}
	m.attributeEarnYield(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "gate", VenueType: "earn", Currency: "USDT", EquityRaw: 1999.6834, FetchedAt: now.Add(3 * time.Minute)},
	})
	if len(fs.events) != 2 || fs.events[1].AmountUSD != -500 {
		t.Fatalf("赎出 Δ 未如实: %+v", fs.events)
	}

	// ⑤D-516 归属系数：BTC 池 share=0.3334 → 收益同乘
	fs.shares = map[string]float64{"binance\x00earn\x00BTC": 0.3334}
	fs.prev["BTC"] = struct {
		eq  float64
		ts  time.Time
		has bool
	}{0.0568, now.Add(-time.Hour), true}
	m.attributeEarnYield(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "binance", VenueType: "earn", Currency: "BTC", EquityRaw: 0.0578, FetchedAt: now.Add(4 * time.Minute)},
	})
	if len(fs.events) != 3 {
		t.Fatalf("系数行未入账: %+v", fs.events)
	}
	want := 0.001 * 0.3334
	if fs.events[2].AmountUSD < want-1e-9 || fs.events[2].AmountUSD > want+1e-9 {
		t.Fatalf("归属系数未乘: got=%v want=%v", fs.events[2].AmountUSD, want)
	}
}
