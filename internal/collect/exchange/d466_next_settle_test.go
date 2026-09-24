package exchange

// D-466 S1：三所 funding_next_settle/funding_interval fact 同帧发出（venue 原生字段实证形制）。
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"arb/internal/fact"
)

func findFact(fs []fact.Fact, kind, venue string) *fact.Fact {
	for i := range fs {
		if fs[i].Kind == kind && fs[i].Venue == venue {
			return &fs[i]
		}
	}
	return nil
}

// Binance premiumIndex nextFundingTime(ms) → funding_next_settle 秒 + fundingInfo 周期秒。
func TestD466_BinanceEmitsNextSettle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"lastFundingRate": "0.0001", "nextFundingTime": float64(1790280000000),
		})
	}))
	defer srv.Close()
	b := NewBinanceFunding(Config{Symbols: []string{"XAU"}, BinanceBaseURL: srv.URL, Client: srv.Client()})
	fiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"symbol": "XAUUSDT", "fundingIntervalHours": 4}})
	}))
	defer fiSrv.Close()
	iv := NewFundingIntervalCache()
	if err := iv.Populate(context.Background(), Config{BinanceBaseURL: fiSrv.URL, Client: fiSrv.Client()}); err != nil {
		t.Fatalf("populate: %v", err)
	}
	b.SetFundingIntervals(iv)
	fs, err := b.Poll(context.Background())
	if err != nil || len(fs) == 0 {
		t.Fatalf("poll: %v fs=%v", err, fs)
	}
	ns := findFact(fs, fact.KindFundingNextSettle, VenueBinance)
	if ns == nil || ns.Symbol != "XAUUSDT" || ns.Value != 1790280000 || ns.Unit != fact.UnitUnixSec {
		t.Fatalf("binance next_settle fact=%+v", ns)
	}
	ivl := findFact(fs, fact.KindFundingInterval, VenueBinance)
	if ivl == nil || ivl.Value != 14400 || ivl.Unit != fact.UnitSeconds {
		t.Fatalf("binance interval fact=%+v（6期/日=14400s）", ivl)
	}
}

// Gate contracts funding_next_apply(unix s)+funding_interval(s) → 双 fact。
func TestD466_GateEmitsNextSettle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"funding_rate": "0.0001", "funding_next_apply": float64(1790280000), "funding_interval": float64(14400),
		})
	}))
	defer srv.Close()
	g := NewGateFunding(Config{Symbols: []string{"XAU"}, GateBaseURL: srv.URL, Client: srv.Client()})
	fs, err := g.Poll(context.Background())
	if err != nil || len(fs) == 0 {
		t.Fatalf("poll: %v fs=%v", err, fs)
	}
	ns := findFact(fs, fact.KindFundingNextSettle, VenueGate)
	if ns == nil || ns.Value != 1790280000 || ns.Unit != fact.UnitUnixSec {
		t.Fatalf("gate next_settle fact=%+v", ns)
	}
	ivl := findFact(fs, fact.KindFundingInterval, VenueGate)
	if ivl == nil || ivl.Value != 14400 {
		t.Fatalf("gate interval fact=%+v", ivl)
	}
}

// OKX fundingTime/nextFundingTime(ms) → next_settle 秒 + 差值间隔秒。
func TestD466_OKXEmitsNextSettle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{
			"fundingRate": "0.0001", "fundingTime": "1790251200000", "nextFundingTime": "1790280000000",
		}}})
	}))
	defer srv.Close()
	o := NewOKXFunding(Config{Symbols: []string{"XAU"}, OKXBaseURL: srv.URL, Client: srv.Client()})
	fs, err := o.Poll(context.Background())
	if err != nil || len(fs) == 0 {
		t.Fatalf("poll: %v fs=%v", err, fs)
	}
	ns := findFact(fs, fact.KindFundingNextSettle, VenueOKX)
	if ns == nil || ns.Value != 1790280000 || ns.Unit != fact.UnitUnixSec {
		t.Fatalf("okx next_settle fact=%+v", ns)
	}
	ivl := findFact(fs, fact.KindFundingInterval, VenueOKX)
	if ivl == nil || ivl.Value != 28800 { // 8h 差值
		t.Fatalf("okx interval fact=%+v（8h=28800s）", ivl)
	}
}
