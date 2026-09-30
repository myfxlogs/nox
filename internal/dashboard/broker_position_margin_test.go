package dashboard

import (
	"arb/internal/venue"
	"context"
	"math"
	"testing"
)

// TestAllocateMargins_Proportional D-124 方案 C：3 笔同 venue + 已知 CS + account margin=$1000
// → 各笔按 notional 比例分摊，和=$1000（守恒）。
func TestAllocateMargins_Proportional(t *testing.T) {
	orders := []venue.Order{
		{Ticket: "1", Symbol: "EURUSD", Volume: 1.0, OpenPrice: 1.10},
		{Ticket: "2", Symbol: "XAUUSD", Volume: 0.1, OpenPrice: 2000.0},
		{Ticket: "3", Symbol: "NAS100", Volume: 0.5, OpenPrice: 15000.0},
	}
	csMap := map[string]float64{
		"EURUSD": 100000, // notional = 1.0 × 100000 × 1.10 = 110000
		"XAUUSD": 100,    // notional = 0.1 × 100 × 2000 = 20000
		"NAS100": 1,      // notional = 0.5 × 1 × 15000 = 7500
	}
	// total notional = 137500
	// margin1 = 1000 × 110000/137500 = 800
	// margin2 = 1000 × 20000/137500 ≈ 145.45
	// margin3 = 1000 × 7500/137500 ≈ 54.55
	margin := allocateMargins(orders, 1000.0, csMap)
	if len(margin) != 3 {
		t.Fatalf("want 3 entries, got %d", len(margin))
	}
	checkClose := func(t *testing.T, got, want float64, label string) {
		t.Helper()
		if math.Abs(got-want) > 0.01 {
			t.Errorf("%s: want %.2f, got %.2f", label, want, got)
		}
	}
	checkClose(t, margin["1"], 800.0, "margin1")
	checkClose(t, margin["2"], 145.45, "margin2")
	checkClose(t, margin["3"], 54.55, "margin3")
	// 守恒：和 = account margin
	var sum float64
	for _, v := range margin {
		sum += v
	}
	checkClose(t, sum, 1000.0, "sum (守恒)")
}

// TestAllocateMargins_CSUnknownAllZero D-124 守恒优先：任一笔 CS 未知 → 该 venue 全部笔 margin=0。
func TestAllocateMargins_CSUnknownAllZero(t *testing.T) {
	orders := []venue.Order{
		{Ticket: "1", Symbol: "EURUSD", Volume: 1.0, OpenPrice: 1.10},
		{Ticket: "2", Symbol: "UNKNOWN", Volume: 0.1, OpenPrice: 2000.0},
	}
	csMap := map[string]float64{
		"EURUSD": 100000,
		// UNKNOWN 缺失 → cs=0
	}
	margin := allocateMargins(orders, 1000.0, csMap)
	if len(margin) != 0 {
		t.Errorf("CS unknown: want 0 entries (all zero), got %d: %v", len(margin), margin)
	}
}

// TestAllocateMargins_CEXZeroMargin D-124：CEX venue account margin=0 → 全 0。
func TestAllocateMargins_CEXZeroMargin(t *testing.T) {
	orders := []venue.Order{
		{Ticket: "1", Symbol: "BTCUSDT", Volume: 0.5, OpenPrice: 60000.0},
	}
	csMap := map[string]float64{"BTCUSDT": 1}
	margin := allocateMargins(orders, 0.0, csMap) // CEX margin=0
	if len(margin) != 0 {
		t.Errorf("CEX zero margin: want 0 entries, got %d: %v", len(margin), margin)
	}
}

// TestAllocateMargins_SinglePosition D-124：单笔 → 100% account margin。
func TestAllocateMargins_SinglePosition(t *testing.T) {
	orders := []venue.Order{
		{Ticket: "1", Symbol: "EURUSD", Volume: 1.0, OpenPrice: 1.10},
	}
	csMap := map[string]float64{"EURUSD": 100000}
	margin := allocateMargins(orders, 500.0, csMap)
	if math.Abs(margin["1"]-500.0) > 0.01 {
		t.Errorf("single position: want 500.0 (100%%), got %.2f", margin["1"])
	}
}

// TestAllocateMargins_EmptyOrders D-124：0 笔 → 空 map。
func TestAllocateMargins_EmptyOrders(t *testing.T) {
	margin := allocateMargins(nil, 1000.0, map[string]float64{"EURUSD": 100000})
	if len(margin) != 0 {
		t.Errorf("empty orders: want 0 entries, got %d", len(margin))
	}
}

// TestBuild_CEXMarginPriority D-146 S5 微件③：CEX perp venue.Order.InitialMargin>0 → 优先于 D-124 分摊。
// 构造 binance:perp venue 带 InitialMargin=600 的订单 → build 后 BrokerPosition.Margin=600（非 0=未知）。
// MT4/MT5 仓 InitialMargin=0 → 走 D-124 分摊（accountMargin=0 时 margin=0，行为不变）。
func TestBuild_CEXMarginPriority(t *testing.T) {
	q := &fakeVenueQuerier{key: "binance:perp", orders: []venue.Order{
		{Ticket: "BTCUSDT", Symbol: "BTCUSDT", Side: "BUY", Volume: 0.1, OpenPrice: 60000, Venue: "binance", VenueKey: "binance:perp", Profit: 1.5, InitialMargin: 600.0, MaintMargin: 48.0},
	}}
	m, _, _ := newMonTest(t, q)
	// 手动注入 venue 状态（跳过 poll，直接测 build）
	m.mu.Lock()
	m.venues["binance:perp"] = venueState{online: true, orders: ordersByTicket(q.orders)}
	m.mu.Unlock()

	pbs := m.build(context.Background())
	if len(pbs) != 1 {
		t.Fatalf("want 1 position, got %d", len(pbs))
	}
	// CEX InitialMargin=600 → BrokerPosition.Margin=600（替换 0=未知语义）
	if math.Abs(pbs[0].Margin-600.0) > 0.01 {
		t.Errorf("CEX margin = %.4f, want 600.0 (venue.Order.InitialMargin priority)", pbs[0].Margin)
	}
}

// TestBuild_CEXMarginZeroStillDash D-146 S5 微件③：CEX 仓 InitialMargin=0（venue 未返回/0 仓）→ margin=0（诚实「—」）。
func TestBuild_CEXMarginZeroStillDash(t *testing.T) {
	q := &fakeVenueQuerier{key: "okx:perp", orders: []venue.Order{
		{Ticket: "ETH-USDT-SWAP", Symbol: "ETH-USDT-SWAP", Side: "BUY", Volume: 40, OpenPrice: 2488.5, Venue: "okx", VenueKey: "okx:perp", Profit: 1.23, InitialMargin: 0},
	}}
	m, _, _ := newMonTest(t, q)
	m.mu.Lock()
	m.venues["okx:perp"] = venueState{online: true, orders: ordersByTicket(q.orders)}
	m.mu.Unlock()

	pbs := m.build(context.Background())
	if len(pbs) != 1 {
		t.Fatalf("want 1 position, got %d", len(pbs))
	}
	// InitialMargin=0 + accountMargin=0 → margin=0（诚实「—」）
	if pbs[0].Margin != 0 {
		t.Errorf("CEX InitialMargin=0 → margin = %.4f, want 0 (honest dash)", pbs[0].Margin)
	}
}
