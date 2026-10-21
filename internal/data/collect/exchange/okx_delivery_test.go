// okx_delivery_test.go —— D-649续④ okx USD_UM 交割订阅面锚：instId 归一形、
// tickers futures 分流（delivCanon 命中→canonical/venueType=futures）、目录裁决。
package exchange

import (
	"context"
	"testing"

	"nox/internal/data/bus"
)

func TestOkxDeliv_InstIDNormalize(t *testing.T) {
	// 毕业档 'ETH-USD_UM-261225'→okx instId 'ETH-USD-261225'（_UM 剥离）。
	if got := DelivInstID("ETH-USD_UM-261225"); got != "ETH-USD-261225" {
		t.Fatalf("instId 归一错: %s", got)
	}
	if got := DelivInstID("ETH-USD-261225"); got != "ETH-USD-261225" {
		t.Fatalf("非 _UM 形被误改: %s", got)
	}
}

// futures 帧分流：delivCanon 命中 → publish canonical=hint 键+venueType=futures。
func TestOkxDeliv_FuturesFrameDispatch(t *testing.T) {
	o := &OKXWebSocket{delivCanon: map[string]string{"ETH-USD-261225": "ETH-USD_UM-261225"}}
	var got bus.Quote
	frame := []byte(`{"arg":{"channel":"tickers","instId":"ETH-USD-261225"},"data":[{"bidPx":"4200","askPx":"4201","ts":"1792563200000"}]}`)
	if !o.handleMessage(context.Background(), frame, func(q bus.Quote) { got = q }) {
		t.Fatal("futures tickers 帧未走 publish 径")
	}
	if got.VenueType != "futures" || got.Symbol != "ETH-USD_UM_261225" && got.Symbol != "ETH-USD_UM-261225" {
		t.Fatalf("futures 帧分流错: %+v", got)
	}
	if got.VenueType != "futures" || got.Symbol != "ETH-USD_UM-261225" || got.Venue != VenueOKX || got.VenueSymbol != "ETH-USD-261225" {
		t.Fatalf("futures 帧 canonical/venue 错: %+v", got)
	}
	if got.Bid != 4200 || got.Ask != 4201 {
		t.Fatalf("futures 帧 bid/ask 错: %+v", got)
	}
}

// 非交割 instId（spot/perp）不受 delivCanon 影响——原分派不变。
func TestOkxDeliv_SpotUnaffected(t *testing.T) {
	o := &OKXWebSocket{delivCanon: map[string]string{"ETH-USD-261225": "ETH-USD_UM-261225"}}
	var got bus.Quote
	frame := []byte(`{"arg":{"channel":"tickers","instId":"ETH-USDT"},"data":[{"bidPx":"4000","askPx":"4001","ts":"1792563200000"}]}`)
	if !o.handleMessage(context.Background(), frame, func(q bus.Quote) { got = q }) {
		t.Fatal("spot 帧未走 publish 径")
	}
	if got.VenueType != "spot" || got.Symbol != "ETHUSDT" {
		t.Fatalf("spot 帧被误改: %+v", got)
	}
}
