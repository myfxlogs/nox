package main

// D-337R1 T1：装配接线锚——wireServerTZ 后 MT5 腿 SwapChargeAt 有值（端到端）。
// 红①=wireServerTZ 现无此函数（编译）；红②=装配断链态 Refresh 后恒 nil（缺陷①复现）。
import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/data/collect/exchange"
	"nox/internal/data/venue"
	"nox/internal/trading/exec"

	v1 "nox/internal/gen/dashboardv1"
)

// fakeTZQuerier MT5 querier stub（持仓+时区，可数调用）。
type fakeTZQuerier struct {
	key     string
	orders  []venue.Order
	tz      float64
	tzErr   error
	tzCalls int
}

func (f *fakeTZQuerier) VenueKey() string { return f.key }
func (f *fakeTZQuerier) QueryOpenedOrders(context.Context) ([]venue.Order, error) {
	return f.orders, nil
}
func (f *fakeTZQuerier) ServerTimezone(context.Context) (float64, error) {
	f.tzCalls++
	return f.tz, f.tzErr
}

func TestD337R1_WireServerTZ(t *testing.T) {
	ctx := context.Background()
	q := &fakeTZQuerier{key: "testvenue:mt5", tz: 3, orders: []venue.Order{
		{Venue: "testvenue", VenueKey: "testvenue:mt5", Ticket: "1", Symbol: "XAUUSD", Side: "SELL", Volume: 0.01, OpenPrice: 4000, Profit: 0},
	}}
	qbus := bus.New()
	bm := dashboard.NewBrokerManager(nil, qbus, exec.NewRegistry(), time.Minute)
	bm.AddStaticQuerier("testvenue", q)
	bpTopic := bus.NewTopic[*v1.BrokerPositionEvent]()
	// D-337R2 S1：构造必选注入（真构造非 nil）；红=构造传 nil（编译红贴输出）/
	// 摘实参（not enough arguments 编译红）——装配断链编译期守卫。
	// D-337R3 T1：cexIntervals 第 8 参构造注入（真构造非 nil）；红=摘实参（编译红贴输出）。
	mon := dashboard.NewBrokerPositionMonitor(bm, qbus, time.Minute, bpTopic,
		dashboard.NewServerTZCache(24*time.Hour, serverTZFetcherFor(bm)), exchange.NewFundingIntervalCache(), nil)

	got := mon.Refresh(ctx)
	if len(got) != 1 {
		t.Fatalf("want 1 position, got %d", len(got))
	}
	if got[0].SwapChargeAt == nil {
		t.Fatalf("MT5 腿 swap_charge_at 应有值（红②=装配断链恒 nil）")
	}
	if q.tzCalls == 0 {
		t.Error("fetcher 应经 BrokerManager.ServerTZQueriers 被调")
	}
}

// TestD337R3_CEXLegBoundary T2：cexIntervals 注入后 CEX 腿出 swapChargeAt——
// ① 4h 真值（httptest mock fundingInfo Populate）→ 下一 4h 边界；
// ② 空 cache（未 Populate）→ D-466R1 nil 诚实空态+warn（业主令禁 8h 兜底猜值）。
func TestD337R3_CEXLegBoundary(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	mkMon := func(intervals *exchange.FundingIntervalCache) (*dashboard.BrokerPositionMonitor, *fakeTZQuerier) {
		q := &fakeTZQuerier{key: "binance:perp", orders: []venue.Order{
			{Venue: "binance", VenueKey: "binance:perp", Ticket: "XAUUSDT", Symbol: "XAUUSDT", Side: "SELL", Volume: 1, OpenPrice: 4355, Profit: 0},
		}}
		qbus := bus.New()
		bm := dashboard.NewBrokerManager(nil, qbus, exec.NewRegistry(), time.Minute)
		bm.AddStaticQuerier(q.key, q)
		return dashboard.NewBrokerPositionMonitor(bm, qbus, time.Minute,
			bus.NewTopic[*v1.BrokerPositionEvent](), dashboard.ServerTZCache{}, intervals, nil), q
	}

	// ① 4h 真值
	fiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"symbol": "XAUUSDT", "fundingIntervalHours": 4}})
	}))
	defer fiSrv.Close()
	intervals := exchange.NewFundingIntervalCache()
	if err := intervals.Populate(ctx, exchange.Config{BinanceBaseURL: fiSrv.URL, Client: fiSrv.Client()}); err != nil {
		t.Fatalf("populate: %v", err)
	}
	mon, _ := mkMon(intervals)
	got := mon.Refresh(ctx)
	if len(got) != 1 || got[0].SwapChargeAt == nil {
		t.Fatalf("4h 真值下 CEX 腿应出结算时刻（红=R1 前 CEX 腿恒 nil）, got %+v", got)
	}
	want4 := dashboard.NextFundingSettlementUTC(now, 6)
	if diff := math.Abs(got[0].SwapChargeAt.AsTime().Sub(want4).Seconds()); diff > 5 {
		t.Errorf("4h 边界 want %v ±5s, got %v", want4, got[0].SwapChargeAt.AsTime())
	}

	// ② 空 cache → D-466R1 nil 诚实空态（禁 8h 兜底=猜的值）
	mon2, _ := mkMon(exchange.NewFundingIntervalCache())
	got2 := mon2.Refresh(ctx)
	if len(got2) != 1 || got2[0].SwapChargeAt != nil {
		t.Fatalf("空 cache want nil 空态（R1 禁 8h 兜底）, got %+v", got2)
	}
}
