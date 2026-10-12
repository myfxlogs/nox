package dashboard

// D-337R1 T2/T3：CEX 腿 funding 结算倒计时+周期费率（DSN）与诚实空态/MT5 回归。
import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/data/collect/exchange"
	"arb/internal/data/fact"
	"arb/internal/data/venue"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/trading/exec"
)

// fakeCEXQuerier CEX querier stub（仅持仓；CEX 无 ServerTZQuerier 实现=nil 空态语义）。
type fakeCEXQuerier struct {
	key    string
	orders []venue.Order
}

func (f *fakeCEXQuerier) VenueKey() string { return f.key }
func (f *fakeCEXQuerier) QueryOpenedOrders(context.Context) ([]venue.Order, error) {
	return f.orders, nil
}

func TestD337R1_CEXLegClockAndRate(t *testing.T) {
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs, fin_xasset_monitor CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// facts：binance XAUUSDT funding 年化 12.35%
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindFunding, Venue: "binance", Symbol: "XAUUSDT", Value: 12.35, Unit: fact.UnitPctAnnualized, Ts: time.Now(), Src: "t"},
	}); err != nil {
		t.Fatalf("seed fact: %v", err)
	}
	// FundingIntervalCache 4h（httptest mock fundingInfo）
	fiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"symbol": "XAUUSDT", "fundingIntervalHours": 4}})
	}))
	defer fiSrv.Close()
	intervals := exchange.NewFundingIntervalCache()
	if err := intervals.Populate(ctx, exchange.Config{BinanceBaseURL: fiSrv.URL, Client: fiSrv.Client()}); err != nil {
		t.Fatalf("populate: %v", err)
	}

	q := &fakeCEXQuerier{key: "binance:perp", orders: []venue.Order{
		{Venue: "binance", VenueKey: "binance:perp", Ticket: "XAUUSDT", Symbol: "XAUUSDT", Side: "SELL", Volume: 1, OpenPrice: 4355, Profit: 0},
	}}
	qb := bus.New()
	bm := NewBrokerManager(nil, qb, exec.NewRegistry(), time.Minute)
	bm.AddStaticQuerier(q.key, q)
	mon := NewBrokerPositionMonitor(bm, qb, time.Minute,
		bus.NewTopic[*v1.BrokerPositionEvent](), NewServerTZCache(time.Minute, func(context.Context, string) (float64, error) {
			return 0, errors.New("n/a")
		}), intervals, nil)
	mon.SetCEXFundingRate(func(_ context.Context, venue, symbol string) (float64, bool) {
		if venue != "binance" || symbol != "XAUUSDT" {
			return 0, false
		}
		fs, err := s.LatestFacts(ctx, "funding", venue, symbol)
		if err != nil || len(fs) == 0 {
			return 0, false
		}
		return fs[0].Value, true
	})

	got := mon.Refresh(ctx)
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	p := got[0]
	// T2a：下次结算边界=下一 4h 整点（now 之后的 00/04/08/12/16/20 UTC 之一）
	if p.SwapChargeAt == nil {
		t.Fatalf("CEX 腿结算时刻应有值（红=现 CEX 分支恒 nil）")
	}
	want := NextFundingSettlementUTC(time.Now().UTC(), 6)
	if diff := math.Abs(p.SwapChargeAt.AsTime().Sub(want).Seconds()); diff > 5 {
		t.Errorf("边界 want %v ±5s, got %v", want, p.SwapChargeAt.AsTime())
	}
	// T2b：周期费率=12.35/(365×6)≈0.005639%
	wantPct := 12.35 / (365 * 6)
	if math.Abs(p.FundingRatePeriodicPct-wantPct) > 1e-9 {
		t.Errorf("周期费率 want %v（红=去 ÷periods 变异实拍 12.35）, got %v", wantPct, p.FundingRatePeriodicPct)
	}
}

func TestD337R1_CEXNoFactZero(t *testing.T) {
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs, fin_xasset_monitor CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	fiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"symbol": "XAUUSDT", "fundingIntervalHours": 4}})
	}))
	defer fiSrv.Close()
	intervals := exchange.NewFundingIntervalCache()
	_ = intervals.Populate(ctx, exchange.Config{BinanceBaseURL: fiSrv.URL, Client: fiSrv.Client()})
	q := &fakeCEXQuerier{key: "binance:perp", orders: []venue.Order{
		{Venue: "binance", VenueKey: "binance:perp", Ticket: "XAUUSDT", Symbol: "XAUUSDT", Side: "SELL", Volume: 1, OpenPrice: 4355, Profit: 0},
	}}
	qb := bus.New()
	bm := NewBrokerManager(nil, qb, exec.NewRegistry(), time.Minute)
	bm.AddStaticQuerier(q.key, q)
	mon := NewBrokerPositionMonitor(bm, qb, time.Minute,
		bus.NewTopic[*v1.BrokerPositionEvent](), NewServerTZCache(time.Minute, func(context.Context, string) (float64, error) {
			return 0, errors.New("n/a")
		}), intervals, nil)
	mon.SetCEXFundingRate(func(_ context.Context, _, _ string) (float64, bool) { return 0, false }) // 无 facts 行
	got := mon.Refresh(ctx)
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if got[0].FundingRatePeriodicPct != 0 {
		t.Errorf("T3 无行费率 want 0（诚实空态）, got %v", got[0].FundingRatePeriodicPct)
	}
	if got[0].SwapChargeAt == nil {
		t.Error("T3 无 facts 行不影响结算时刻（周期独立于费率）")
	}
}
