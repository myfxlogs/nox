package dashboard

import (
	"math"
	"testing"

	"arb/internal/reconcile"
)

// TestAllocateMargins_Proportional D-124 方案 C：3 笔同 venue + 已知 CS + account margin=$1000
// → 各笔按 notional 比例分摊，和=$1000（守恒）。
func TestAllocateMargins_Proportional(t *testing.T) {
	orders := []reconcile.VenueOrder{
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
	orders := []reconcile.VenueOrder{
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
	orders := []reconcile.VenueOrder{
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
	orders := []reconcile.VenueOrder{
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
