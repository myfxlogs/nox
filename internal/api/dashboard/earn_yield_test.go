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
	// flows 按 (venue,ccy) 行级窗内申购流——key=ccy（单池单币，测试域足够）。
	flows   map[string]float64
	shares  map[string]float64
	events  []pgstore.IncomeEvent
	orders  []pgstore.EarnPoolOrder
	yields  float64
	actuals map[int64]float64
	// DCI 台账对拍注面。
	dciOrders []pgstore.FilledDCIOrder
	alerts    []string
}

func (f *fakeYieldStore) PrevSnapshotEquity(_ context.Context, _, _, ccy string, _ time.Time) (float64, time.Time, bool, error) {
	p := f.prev[ccy]
	return p.eq, p.ts, p.has, nil
}
func (f *fakeYieldStore) EarnSubscribeSum(_ context.Context, _, ccy string, _, _ time.Time) (float64, error) {
	return f.flows[ccy], nil
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
func (f *fakeYieldStore) ListEarnPoolOrders(context.Context, string, string) ([]pgstore.EarnPoolOrder, error) {
	return f.orders, nil
}
func (f *fakeYieldStore) SumEarnYieldSince(context.Context, string, string, time.Time) (float64, error) {
	return f.yields, nil
}
func (f *fakeYieldStore) ListFilledDCIOrders(context.Context) ([]pgstore.FilledDCIOrder, error) {
	return f.dciOrders, nil
}
func (f *fakeYieldStore) InsertAlertDedup(_ context.Context, level, message string, _ time.Duration) error {
	f.alerts = append(f.alerts, level+"|"+message)
	return nil
}
func (f *fakeYieldStore) UpdateOppEvalActual(_ context.Context, pid int64, apr float64) error {
	if f.actuals == nil {
		f.actuals = map[int64]float64{}
	}
	f.actuals[pid] = apr
	return nil
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

	// ②申购流入+x 后的池行：Δ=x 全为流入 → 零收益行（流按 ccy 行级查）
	fs.prev["USDT"] = struct {
		eq  float64
		ts  time.Time
		has bool
	}{1999.64, now.Add(-time.Hour), true}
	fs.flows = map[string]float64{"USDT": 500} // 同拍内本系统申购 +500（Δ 全冲销）
	m.attributeEarnYield(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "gate", VenueType: "earn", Currency: "USDT", EquityRaw: 2499.64, FetchedAt: now.Add(time.Minute)},
	})
	if len(fs.events) != 0 {
		t.Fatalf("申购流入误归收益: %+v", fs.events)
	}

	// ③复投增量：池 +0.0434 无申购流 → earn_yield=+0.0434
	fs.flows = nil
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

	// ⑤D-516 归属系数：BTC 池 share=0.3334 → 收益同乘（Δ=0.03 过零额滤）
	fs.shares = map[string]float64{"binance\x00earn\x00BTC": 0.3334}
	fs.prev["BTC"] = struct {
		eq  float64
		ts  time.Time
		has bool
	}{0.0568, now.Add(-time.Hour), true}
	m.attributeEarnYield(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "binance", VenueType: "earn", Currency: "BTC", EquityRaw: 0.0868, FetchedAt: now.Add(4 * time.Minute)},
	})
	if len(fs.events) != 3 {
		t.Fatalf("系数行未入账: %+v", fs.events)
	}
	want := 0.03 * 0.3334
	if fs.events[2].AmountUSD < want-1e-9 || fs.events[2].AmountUSD > want+1e-9 {
		t.Fatalf("归属系数未乘: got=%v want=%v", fs.events[2].AmountUSD, want)
	}
}

// TestEarnYieldNoFanout 扇出回归锚：venue 级单币（USDT）申购流只落 USDT 自身行，
// 同批非申购币种行零扇入（binance −$30k 假账病灶——逐币行扣 venue 级汇总）。
func TestEarnYieldNoFanout(t *testing.T) {
	fs := &fakeYieldStore{prev: map[string]struct {
		eq  float64
		ts  time.Time
		has bool
	}{}}
	m := &AccountMonitor{yieldStore: fs}
	ctx := context.Background()
	now := time.Now()

	fs.flows = map[string]float64{"USDT": 351.64} // venue 级流只属 USDT 池
	fs.prev["USDT"] = struct {
		eq  float64
		ts  time.Time
		has bool
	}{1000.0, now.Add(-time.Hour), true}
	fs.prev["DOGE"] = struct {
		eq  float64
		ts  time.Time
		has bool
	}{52886.43, now.Add(-time.Hour), true}
	m.attributeEarnYield(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "binance", VenueType: "earn", Currency: "USDT", EquityRaw: 1351.66, FetchedAt: now},  // 申购 351.64+真收益 0.02
		{Venue: "binance", VenueType: "earn", Currency: "DOGE", EquityRaw: 52886.43, FetchedAt: now}, // 持平——病灶下=−351.64 假账行
	})
	if len(fs.events) != 1 {
		t.Fatalf("扇出未绝: %+v", fs.events)
	}
	if fs.events[0].Symbol != "USDT" || fs.events[0].AmountUSD < 0.019 || fs.events[0].AmountUSD > 0.021 {
		t.Fatalf("USDT 行口径错: %+v", fs.events[0])
	}
}

// TestEarnYieldZeroNotRecorded 零额不落锚：写值 |Δ|<0.005（展示 0.00 界）不入账。
func TestEarnYieldZeroNotRecorded(t *testing.T) {
	fs := &fakeYieldStore{prev: map[string]struct {
		eq  float64
		ts  time.Time
		has bool
	}{}}
	m := &AccountMonitor{yieldStore: fs}
	ctx := context.Background()
	now := time.Now()

	fs.prev["DOGE"] = struct {
		eq  float64
		ts  time.Time
		has bool
	}{52886.43, now.Add(-time.Hour), true}
	fs.prev["USDT"] = struct {
		eq  float64
		ts  time.Time
		has bool
	}{1000.0, now.Add(-time.Hour), true}
	m.attributeEarnYield(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "binance", VenueType: "earn", Currency: "DOGE", EquityRaw: 52886.4339, FetchedAt: now}, // +0.0039
		{Venue: "binance", VenueType: "earn", Currency: "USDT", EquityRaw: 1000.0042, FetchedAt: now},  // +0.0042
	})
	if len(fs.events) != 0 {
		t.Fatalf("零额行误入账: %+v", fs.events)
	}
	m.attributeEarnYield(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "binance", VenueType: "earn", Currency: "USDT", EquityRaw: 1000.0051, FetchedAt: now}, // +0.0051 过界
	})
	if len(fs.events) != 1 || fs.events[0].AmountUSD < 0.005 {
		t.Fatalf("过界行未入账: %+v", fs.events)
	}
}

// TestEarnActualAPRWriteback D-527①③：收益行落账同拍→池实现 APR 回写提案卡。
func TestEarnActualAPRWriteback(t *testing.T) {
	fs := &fakeYieldStore{prev: map[string]struct {
		eq  float64
		ts  time.Time
		has bool
	}{}}
	m := &AccountMonitor{yieldStore: fs}
	ctx := context.Background()
	now := time.Now()

	// 提案单在场（qty=500、2 天前申购）+窗内收益 5 → apr=5/500×365/2×100=182.5。
	fs.orders = []pgstore.EarnPoolOrder{{ProposalID: 9, Qty: 500, CreatedAt: now.Add(-48 * time.Hour)}}
	fs.yields = 5
	fs.prev["USDT"] = struct {
		eq  float64
		ts  time.Time
		has bool
	}{1999.6, now.Add(-time.Minute), true}
	m.attributeEarnYield(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "gate", VenueType: "earn", Currency: "USDT", EquityRaw: 1999.7, FetchedAt: now},
	})
	want := 5.0 / 500 * 365 / 2 * 100
	if got := fs.actuals[9]; got < want*0.98 || got > want*1.02 {
		t.Fatalf("actual_apr=%v want≈%v", got, want)
	}

	// 负收益如实负 APR（赎回/划转负 Δ）；末次事件覆盖=累计重算。
	fs.yields = -2.5
	fs.prev["USDT"] = struct {
		eq  float64
		ts  time.Time
		has bool
	}{1999.7, now.Add(-time.Minute), true}
	m.attributeEarnYield(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "gate", VenueType: "earn", Currency: "USDT", EquityRaw: 1999.0, FetchedAt: now.Add(time.Minute)},
	})
	if got := fs.actuals[9]; got >= 0 {
		t.Fatalf("负收益未如实负 APR: %v", got)
	}

	// 无提案回链单 → 收益行照记但卡不回写（无对应执行事件不编数）。
	fs.orders = nil
	fs.prev["USDT"] = struct {
		eq  float64
		ts  time.Time
		has bool
	}{1999.0, now.Add(-time.Minute), true}
	m.attributeEarnYield(ctx, []pgstore.VenueAccountSnapshot{
		{Venue: "gate", VenueType: "earn", Currency: "USDT", EquityRaw: 1999.1, FetchedAt: now.Add(2 * time.Minute)},
	})
	if len(fs.actuals) != 1 { // 仅前两拍写过的 #9
		t.Fatalf("无回链误写: %+v", fs.actuals)
	}
}
