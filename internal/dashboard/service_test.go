package dashboard

import (
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// TestResolveLegs 验证 resolveLegs 从 qbus 取双腿 broker 原生符号（D-051 修②）：
// VenueSymbol 正确、Side/Key/Qty 正确、双腿齐备。
func TestResolveLegs(t *testing.T) {
	b := bus.New()
	now := time.Now()
	b.Publish(bus.Quote{Symbol: "EURUSD", Venue: "b", VenueType: "mt5", Bid: 1.0, Ask: 1.0001, Time: now, VenueSymbol: "EURUSDm"})
	b.Publish(bus.Quote{Symbol: "EURUSD", Venue: "s", VenueType: "mt5", Bid: 1.0002, Ask: 1.0003, Time: now, VenueSymbol: "EURUSDm"})

	s := &Service{qbus: b}
	found := &pgstore.Opportunity{Canonical: "EURUSD", VenueBuy: "b:mt5", VenueSell: "s:mt5"}

	legs, err := s.resolveLegs(found, 0.1)
	if err != nil {
		t.Fatalf("resolveLegs unexpected error: %v", err)
	}
	if len(legs) != 2 {
		t.Fatalf("want 2 legs, got %d", len(legs))
	}

	buy, sell := legs[0], legs[1]
	if buy.VenueSymbol != "EURUSDm" || sell.VenueSymbol != "EURUSDm" {
		t.Errorf("VenueSymbol want EURUSDm for both, got %q / %q", buy.VenueSymbol, sell.VenueSymbol)
	}
	if buy.VenueKey != "b:mt5" || sell.VenueKey != "s:mt5" {
		t.Errorf("VenueKey want b:mt5 / s:mt5, got %q / %q", buy.VenueKey, sell.VenueKey)
	}
	if buy.Side != "BUY" || sell.Side != "SELL" {
		t.Errorf("Side want BUY / SELL, got %q / %q", buy.Side, sell.Side)
	}
	if buy.Qty != 0.1 || sell.Qty != 0.1 {
		t.Errorf("Qty want 0.1 / 0.1, got %v / %v", buy.Qty, sell.Qty)
	}
}

// TestResolveLegsNoQuote 缺任一腿报价 → 返回错误（执行方 FAILED 透传）。
func TestResolveLegsNoQuote(t *testing.T) {
	b := bus.New()
	now := time.Now()
	// 只播买腿，卖腿无报价。
	b.Publish(bus.Quote{Symbol: "EURUSD", Venue: "b", VenueType: "mt5", Bid: 1.0, Ask: 1.0001, Time: now, VenueSymbol: "EURUSDm"})

	s := &Service{qbus: b}
	found := &pgstore.Opportunity{Canonical: "EURUSD", VenueBuy: "b:mt5", VenueSell: "s:mt5"}

	if _, err := s.resolveLegs(found, 0.1); err == nil {
		t.Fatal("missing sell leg want error, got nil")
	}
}
