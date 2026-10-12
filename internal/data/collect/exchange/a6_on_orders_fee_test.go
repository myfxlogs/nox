package exchange

// d570b_gate_test.go —— D-570 包B 验收锚 A6（onOrders fee 回填）。
import (
	"context"
	"testing"
	"time"
)

// A6：futures.orders 帧 fee=JSON number → PutOrder FeeUSD 回填 + consumed 含 fee。
func TestD570B_A6_OnOrdersFee(t *testing.T) {
	oc := NewOrderEventCache()
	g := &GatePrivateWS{
		venueKey:   "gate:perp",
		orderCache: oc,
		quanto:     map[string]float64{"BTC_USDT": 1},
		now:        func() time.Time { return time.Now() },
	}
	g.onOrders(context.Background(), []byte(`[{"id":55,"text":"t-a1","contract":"BTC_USDT","status":"finished","finish_as":"filled","size":10,"left":0,"fill_price":"100","fee":0.0009}]`))
	ev, ok := oc.PeekOrder("gate:perp", "55")
	if !ok {
		t.Fatalf("PeekOrder 应命中")
	}
	if ev.FeeUSD != 0.0009 {
		t.Fatalf("FeeUSD 应=帧 fee 0.0009（fee 字段漏填→本锚必红）, got %v", ev.FeeUSD)
	}
	found := false
	for _, f := range gateOrdersConsumed {
		if f == "fee" {
			found = true
		}
	}
	if !found {
		t.Fatalf("gateOrdersConsumed 应含 fee")
	}
}
