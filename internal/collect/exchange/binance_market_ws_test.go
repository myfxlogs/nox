// binance_market_ws_test.go —— D-505 验收：双 payload 形制解析表驱动
// （perp 带 e/T/E 包裹、spot 裸 u/s/b/B/a/A——实测钉死差异防席位踩坑）。
// ack/错误帧分流+时间戳回退（T 缺→本地 now）一并钉死。
package exchange

import (
	"testing"
	"time"

	"arb/internal/bus"
)

func d505WS() *BinanceMarketWS {
	b := NewBinanceMarketWS(Config{Symbols: []string{"XAU", "BTC"}})
	b.now = func() time.Time { return time.Unix(1790400009, 0) } // 本地兜底锚（与帧 T 区分）
	return b
}

func TestD505_HandleBookTicker_DualPayload(t *testing.T) {
	cases := []struct {
		name      string
		payload   string
		venueType string
		wantSym   string
		wantBid   float64
		wantAsk   float64
		wantTs    time.Time
	}{
		{"perp 形（e/T/E 包裹，T=帧内毫秒）", `{"e":"bookTicker","u":1,"s":"XAUUSDT","ps":"XAUUSDT","b":"4287.95","B":"2.5","a":"4288.10","A":"1.5","T":1790400000000,"E":1790400000001}`, "perp", "XAUUSDT", 4287.95, 4288.10, time.UnixMilli(1790400000000)},
		{"spot 形（裸字段无 e/T/E，T 零→本地 now）", `{"u":2,"s":"BTCUSDT","b":"60000.10","B":"1.0","a":"60000.50","A":"2.0"}`, "spot", "BTCUSDT", 60000.10, 60000.50, time.Unix(1790400009, 0)},
	}
	for _, tc := range cases {
		b := d505WS()
		var got *bus.Quote
		b.handleBookTicker([]byte(tc.payload), tc.venueType, func(q bus.Quote) {
			c := q
			got = &c
		})
		if got == nil {
			t.Fatalf("%s: 未发布", tc.name)
		}
		if got.Symbol != tc.wantSym || got.VenueType != tc.venueType || got.Venue != "binance" || got.VenueSymbol != tc.wantSym {
			t.Errorf("%s: 键形=%+v", tc.name, *got)
		}
		if got.Bid != tc.wantBid || got.Ask != tc.wantAsk {
			t.Errorf("%s: bid/ask=%v/%v want %v/%v", tc.name, got.Bid, got.Ask, tc.wantBid, tc.wantAsk)
		}
		if !got.Time.Equal(tc.wantTs) {
			t.Errorf("%s: 时间戳=%v want %v（perp=帧 T 毫秒/spot=本地 now）", tc.name, got.Time, tc.wantTs)
		}
	}
}

// TestD505_AckAndErrorFrames 订阅 ack {result:null,id:1} 与错误帧分流（不产报价）。
func TestD505_AckAndErrorFrames(t *testing.T) {
	b := d505WS()
	cases := []struct {
		name    string
		payload string
		wantAck bool
	}{
		{"ack 帧", `{"result":null,"id":1}`, true},
		{"错误帧", `{"result":null,"id":1,"error":{"code":-1121,"msg":"Invalid symbol."}}`, true},
		{"数据帧非 ack", `{"u":2,"s":"BTCUSDT","b":"1","a":"2"}`, false},
	}
	for _, tc := range cases {
		if got := isSubscribeAck([]byte(tc.payload)); got != tc.wantAck {
			t.Errorf("%s: isSubscribeAck=%v want %v", tc.name, got, tc.wantAck)
		}
	}
	b.handleBookTicker([]byte(`{"result":null,"id":1,"error":{"code":-1121,"msg":"x"}}`), "perp", func(bus.Quote) {
		t.Error("ack 帧不得产报价")
	})
}
