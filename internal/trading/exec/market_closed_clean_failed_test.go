// d029_market_closed_test.go —— D-029 ③：MT 下单回报 MARKET_CLOSED → 干净 FAILED
// （非 UNKNOWN——不进 LEGGED 统计；isContextTimeoutErr 不误判）。
package exec

import (
	"context"
	"testing"
)

// fakeMTMarketClosedExecutor PlaceOrder 恒返「market closed」形态错误（10018 路径）。
type fakeMTMarketClosedExecutor struct{ venue string }

func (e *fakeMTMarketClosedExecutor) Key() string { return e.venue }
func (e *fakeMTMarketClosedExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return Fill{}, nil
}
func (e *fakeMTMarketClosedExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	return Fill{}, errMarketClosed("mt5 ordersend: market closed (10018): Market closed")
}

// errMarketClosed D-029 ③ 类型化：普通 error（非 timeout/非 ErrFillUnconfirmed）——
// executeLeg 判定链据此落 FAILED。
type errMarketClosed string

func (e errMarketClosed) Error() string { return string(e) }

// TestD029_MarketClosedCleanFailed 10018 → FAILED（确定未成交），且非 timeout/UNKNOWN 形态。
func TestD029_MarketClosedCleanFailed(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&fakeMTMarketClosedExecutor{venue: "3001157:mt5"})
	p := &Pipeline{registry: reg}
	p.SetContractSizeResolver(permissiveCSResolver{})

	res := p.executeLeg(context.Background(), LegSpec{
		VenueKey: "3001157:mt5", VenueSymbol: "XAUUSDm", Side: "SELL", Qty: 0.01, PriceExpected: 4200,
	}, "cid-d029")
	if res.Status != "FAILED" {
		t.Fatalf("market closed 应干净 FAILED（禁 UNKNOWN→CLOSING 链），got %q", res.Status)
	}
	err := errMarketClosed("x")
	if isContextTimeoutErr(err) {
		t.Fatal("market closed 不得被判为 timeout（否则 UNKNOWN 化）")
	}
}
