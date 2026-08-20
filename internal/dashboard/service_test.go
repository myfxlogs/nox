package dashboard

import (
	"math"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// TestResolveLegs 验证 resolveLegs 从 qbus 取双腿 broker 原生符号（D-051 修②）：
// VenueSymbol 正确、Side/Key/Qty 正确、双腿齐备。
func TestResolveLegs(t *testing.T) {
	b := bus.New()
	now := time.Now()
	b.Publish(bus.Quote{Symbol: "EURUSD", Venue: "b", VenueType: "mt5", Bid: 1.0, Ask: 1.0001, Time: now, VenueSymbol: "EURUSDm"})
	b.Publish(bus.Quote{Symbol: "EURUSD", Venue: "s", VenueType: "mt5", Bid: 1.0002, Ask: 1.0003, Time: now, VenueSymbol: "EURUSDm"})

	s := &Service{qbus: b, legContractSize: func(_, sym string) (float64, bool) { return 100000, true }}
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
	if buy.PriceExpected != 1.0001 || sell.PriceExpected != 1.0002 {
		t.Errorf("PriceExpected want 1.0001 / 1.0002, got %v / %v", buy.PriceExpected, sell.PriceExpected)
	}
}

// TestResolveLegsNotionalScaling D-060 实施修正②：不等 CS（油 1000:100 桶/手）卖腿等名义反算
// 10×；CS 缺失拒；未接线拒；取整致不平衡超限拒。
func TestResolveLegsNotionalScaling(t *testing.T) {
	b := bus.New()
	now := time.Now()
	b.Publish(bus.Quote{Symbol: "BRENT", Venue: "b", VenueType: "mt5", Bid: 93.8, Ask: 93.8, Time: now, VenueSymbol: "UKOILm"})
	b.Publish(bus.Quote{Symbol: "BRENT", Venue: "s", VenueType: "mt5", Bid: 93.8, Ask: 93.8, Time: now, VenueSymbol: "XBRUSD"})
	found := &pgstore.Opportunity{Canonical: "BRENT", VenueBuy: "b:mt5", VenueSell: "s:mt5"}

	cs := map[string]float64{"UKOILm": 1000, "XBRUSD": 100}
	s := &Service{qbus: b, legContractSize: func(_, sym string) (float64, bool) { v, ok := cs[sym]; return v, ok }}

	legs, err := s.resolveLegs(found, 0.1)
	if err != nil {
		t.Fatalf("oil pair: %v", err)
	}
	if legs[1].Qty != 1.0 {
		t.Errorf("sell qty want 1.0 (0.1×1000/100), got %v", legs[1].Qty)
	}

	// CS 缺失 → 拒。
	s.legContractSize = func(_, _ string) (float64, bool) { return 0, false }
	if _, err := s.resolveLegs(found, 0.1); err == nil || !strings.Contains(err.Error(), "contract size unknown") {
		t.Errorf("missing CS want reject, got %v", err)
	}

	// 未接线 → 拒。
	s.legContractSize = nil
	if _, err := s.resolveLegs(found, 0.1); err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Errorf("not wired want reject, got %v", err)
	}

	// 取整致不平衡 >2% → 拒（CS 1:6，qty 0.1 → sell 0.02，名义差 20%）。
	b2 := bus.New()
	b2.Publish(bus.Quote{Symbol: "X", Venue: "b", VenueType: "mt5", Bid: 100, Ask: 100, Time: now, VenueSymbol: "A"})
	b2.Publish(bus.Quote{Symbol: "X", Venue: "s", VenueType: "mt5", Bid: 100, Ask: 100, Time: now, VenueSymbol: "B"})
	cs2 := map[string]float64{"A": 1, "B": 6}
	s2 := &Service{qbus: b2, legContractSize: func(_, sym string) (float64, bool) { v, ok := cs2[sym]; return v, ok }}
	f2 := &pgstore.Opportunity{Canonical: "X", VenueBuy: "b:mt5", VenueSell: "s:mt5"}
	if _, err := s2.resolveLegs(f2, 0.1); err == nil || !strings.Contains(err.Error(), "imbalance") {
		t.Errorf("imbalance want reject, got %v", err)
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

// TestCheckTradability D-060 F5 执行预检：禁交易腿拒绝（错误含 venue/符号/mode）、
// 未知放行、CEX 腿跳过、nil 未接线放行。
func TestCheckTradability(t *testing.T) {
	found := &pgstore.Opportunity{Canonical: "BTCUSDT", VenueBuy: "ic:mt5", VenueSell: "pxbt:mt5"}

	// 买腿 Full、卖腿 Disabled（PXBT 实证形态）→ 拒绝，错误含 venue+符号+mode。
	s := &Service{tradeModes: func(canonical, venueKey string) (int64, bool) {
		if venueKey == "pxbt:mt5" {
			return fact.TradeModeDisabled, true
		}
		return fact.TradeModeFull, true
	}}
	err := s.checkTradability(found)
	if err == nil {
		t.Fatal("禁交易卖腿应拒绝")
	}
	for _, want := range []string{"pxbt:mt5", "BTCUSDT", "0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息缺 %q: %v", want, err)
		}
	}

	// 卖腿 ShortOnly（可开空）→ 放行。
	s.tradeModes = func(_, vk string) (int64, bool) {
		if vk == "pxbt:mt5" {
			return fact.TradeModeShortOnly, true
		}
		return fact.TradeModeFull, true
	}
	if err := s.checkTradability(found); err != nil {
		t.Fatalf("卖腿 ShortOnly 应放行, got %v", err)
	}

	// 未知（TTL 过期/无 Fact）→ 放行。
	s.tradeModes = func(_, _ string) (int64, bool) { return 0, false }
	if err := s.checkTradability(found); err != nil {
		t.Fatalf("未知应放行, got %v", err)
	}

	// CEX 腿跳过（trade_mode 仅 MT 语义）：即便返回 Disabled 也不拒。
	cex := &pgstore.Opportunity{Canonical: "BTCUSDT", VenueBuy: "binance:spot", VenueSell: "okx:perp"}
	s.tradeModes = func(_, _ string) (int64, bool) { return fact.TradeModeDisabled, true }
	if err := s.checkTradability(cex); err != nil {
		t.Fatalf("CEX 腿应跳过预检, got %v", err)
	}

	// nil = 未接线 → 放行。
	s.tradeModes = nil
	if err := s.checkTradability(found); err != nil {
		t.Fatalf("未接线应放行, got %v", err)
	}
}

// TestNetUSD：净收益 bps → USD（D-053）。
// 价差型=一次性 USD（bps/10000×notional）；息差型=年化 USD/年（pct/100×notional）；notional≤0 不换算。
func TestNetUSD(t *testing.T) {
	cases := []struct {
		name     string
		netBps   float64
		typ      string
		notional float64
		want     float64
	}{
		{"cross one-shot", 48.2, "CROSS_VENUE", 1000, 4.82},
		{"cross zero bps", 0, "CROSS_VENUE", 1000, 0},
		{"carry annualized", 5.2, "CARRY", 1000, 52.0},
		{"funding annualized", 12.0, "FUNDING_HEDGE", 1000, 120.0},
		{"no notional", 48.2, "CROSS_VENUE", 0, 0},
		{"negative bps", -2, "CROSS_VENUE", 1000, -0.2},
	}
	for _, c := range cases {
		got := netUSD(c.netBps, c.typ, c.notional)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: netUSD(%v,%q,%v) want %v, got %v", c.name, c.netBps, c.typ, c.notional, c.want, got)
		}
	}
}

// TestResolveLegsVolumePreflight D-061 量规格预检：qty 低于 venue 最小手数 → 拒（买/卖腿同检）；
// 未知（ok=false）放行；CEX 腿跳过。
func TestResolveLegsVolumePreflight(t *testing.T) {
	b := bus.New()
	now := time.Now()
	b.Publish(bus.Quote{Symbol: "BRENT", Venue: "b", VenueType: "mt5", Bid: 95, Ask: 95, Time: now, VenueSymbol: "UKOILm"})
	b.Publish(bus.Quote{Symbol: "BRENT", Venue: "s", VenueType: "mt5", Bid: 95, Ask: 95, Time: now, VenueSymbol: "BRENT"})
	found := &pgstore.Opportunity{Canonical: "BRENT", VenueBuy: "b:mt5", VenueSell: "s:mt5"}

	cs := map[string]float64{"UKOILm": 1000, "BRENT": 1000}
	s := &Service{qbus: b,
		legContractSize: func(_, sym string) (float64, bool) { v, ok := cs[sym]; return v, ok },
		legMinLots:      func(_, sym string) (float64, bool) { if sym == "BRENT" { return 1.0, true }; return 0.01, true },
	}
	// qty 0.05：卖腿 BRENT min=1.0 → 拒。
	if _, err := s.resolveLegs(found, 0.05); err == nil || !strings.Contains(err.Error(), "below venue min lot") {
		t.Errorf("sell below min want reject, got %v", err)
	}
	// qty 1.0：双腿合法 → 过。
	if _, err := s.resolveLegs(found, 1.0); err != nil {
		t.Errorf("qty 1.0 want pass, got %v", err)
	}
	// 未知（ok=false）→ 放行。
	s.legMinLots = func(_, _ string) (float64, bool) { return 0, false }
	if _, err := s.resolveLegs(found, 0.05); err != nil {
		t.Errorf("unknown min want pass, got %v", err)
	}
}
