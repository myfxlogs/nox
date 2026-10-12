// binance_market_ws_test.go —— D-505 验收：双 payload 形制解析表驱动
// （perp 带 e/T/E 包裹、spot 裸 u/s/b/B/a/A——实测钉死差异防席位踩坑）。
// ack/错误帧分流+时间戳回退（T 缺→本地 now）一并钉死。
package exchange

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/data/fact"
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

// !markPrice@arr@1s 全市场聚合流——数组帧与单帧同构双形解析+有效集过滤
// （全市场 ~400 符号/1s 不过滤=DB 灌水；集过滤即原逐符号订阅集语义保持）。
func d582WS() *BinanceMarketWS {
	b := NewBinanceMarketWS(Config{Symbols: []string{"SKLUSDT"}})
	b.now = func() time.Time { return time.Unix(1790400009, 0) }
	return b
}

// TestMarkPriceAggregateArray 数组帧（集外 ZZZUSDT 在前+集内 SKLUSDT 在后）→
// 只发 SKLUSDT 三 fact（过滤实证）；整帧消费 return true 不泄漏 bookTicker 面。
func TestMarkPriceAggregateArray(t *testing.T) {
	b := d582WS()
	var got [][]fact.Fact
	b.SetFactEmit(func(ctx context.Context, fs []fact.Fact) error {
		got = append(got, fs)
		return nil
	})
	frame := `[{"e":"markPriceUpdate","E":1790400000000,"s":"ZZZUSDT","p":"1.00","r":"0.00010000","T":1790403600000,"i":"1.01"},` +
		`{"e":"markPriceUpdate","E":1790400000000,"s":"SKLUSDT","p":"0.05","r":"0.00005000","T":1790403600000,"i":"0.049"}]`
	if !b.handleMarkPrice(context.Background(), []byte(frame)) {
		t.Fatal("数组帧未被消费（聚合帧应 return true 不泄漏行情面）")
	}
	if len(got) != 1 {
		t.Fatalf("emit calls want 1, got %d", len(got))
	}
	// 含 i 帧 → 第 4 fact=index_price 同帧落库（一帧一钟）。
	if len(got[0]) != 4 {
		t.Fatalf("facts want 4 (funding/next_settle/interval/index_price), got %d: %+v", len(got[0]), got[0])
	}
	for _, f := range got[0] {
		if f.Symbol != "SKLUSDT" {
			t.Fatalf("集外符号泄漏: %+v", f)
		}
	}
	if v := got[0][0].Value; math.Abs(v-5.475) > 1e-9 { // 0.00005000×3/天×365×100
		t.Fatalf("annualized funding want 5.475, got %v", v)
	}
	if f := got[0][3]; f.Kind != fact.KindIndexPrice || f.Value != 0.049 || f.Unit != fact.UnitUSD {
		t.Fatalf("index fact want index_price=0.049, got %+v", f)
	}
}

// TestMarkPriceIndexAbsentTolerated R2 反面：帧缺 i → 不发 index fact，funding 三件套不受累。
func TestMarkPriceIndexAbsentTolerated(t *testing.T) {
	b := d582WS()
	var got [][]fact.Fact
	b.SetFactEmit(func(ctx context.Context, fs []fact.Fact) error {
		got = append(got, fs)
		return nil
	})
	frame := `[{"e":"markPriceUpdate","s":"SKLUSDT","r":"0.00005000","T":1790403600000}]`
	if !b.handleMarkPrice(context.Background(), []byte(frame)) {
		t.Fatal("帧未消费")
	}
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("缺 i 应 3 facts（无 index_price）, got %+v", got)
	}
}

// TestMarkPriceSingleFrameCompat 原单对象帧回归（逐符号订阅兼容路径零变）。
func TestMarkPriceSingleFrameCompat(t *testing.T) {
	b := d582WS()
	var got [][]fact.Fact
	b.SetFactEmit(func(ctx context.Context, fs []fact.Fact) error {
		got = append(got, fs)
		return nil
	})
	frame := `{"e":"markPriceUpdate","E":1790400000000,"s":"SKLUSDT","p":"0.05","r":"0.00005000","T":1790403600000}`
	if !b.handleMarkPrice(context.Background(), []byte(frame)) {
		t.Fatal("单帧未消费")
	}
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("want 1 emit × 3 facts, got %d emits", len(got))
	}
}

// TestMarkPriceEmptyArray 空数组帧消费不炸（零 emit、return true）。
func TestMarkPriceEmptyArray(t *testing.T) {
	b := d582WS()
	var got [][]fact.Fact
	b.SetFactEmit(func(ctx context.Context, fs []fact.Fact) error {
		got = append(got, fs)
		return nil
	})
	if !b.handleMarkPrice(context.Background(), []byte(`[]`)) {
		t.Fatal("空数组帧应消费（return true）")
	}
	if len(got) != 0 {
		t.Fatalf("空数组不应 emit, got %d", len(got))
	}
}

// TestD591_Ticker24hLanding D-591 S6：24hrTicker 数组帧→vol24h facts（全市场不过滤）
// + 每符号 15min 节流（同帧重放零重复发射）。
func TestD591_Ticker24hLanding(t *testing.T) {
	b := d582WS()
	var got [][]fact.Fact
	b.SetFactEmit(func(ctx context.Context, fs []fact.Fact) error {
		got = append(got, fs)
		return nil
	})
	frame := `[{"e":"24hrTicker","E":1790400000000,"s":"SKLUSDT","q":"12345.6"},{"e":"24hrTicker","s":"ZZZUSDT","q":"999"}]`
	b.handleTicker(context.Background(), []byte(frame))
	if len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("want 1 emit × 2 facts, got %d emits", len(got))
	}
	f := got[0][0]
	if f.Kind != "vol24h" || f.Symbol != "SKLUSDT" || f.Value != 12345.6 || f.Unit != "usd" || f.Src != "binance_ws_ticker" {
		t.Fatalf("fact 字段不对: %+v", f)
	}
	b.handleTicker(context.Background(), []byte(frame))
	if len(got) != 1 {
		t.Fatalf("15min 节流失效：重放应零发射, got %d", len(got))
	}
}

// TestD595_IndexPriceThrottle D-595 S1：index_price per-symbol 10min 心跳节流——
// 10min 内第二帧（值变）不写、过 10min 写（先红后绿；vol24h 15min 节流先例同构）。
func TestD595_IndexPriceThrottle(t *testing.T) {
	b := d582WS()
	var nowMu atomic.Int64 // 毫秒时钟（可推进）
	b.now = func() time.Time { return time.UnixMilli(nowMu.Load()) }
	var got [][]fact.Fact
	b.SetFactEmit(func(ctx context.Context, fs []fact.Fact) error {
		got = append(got, fs)
		return nil
	})
	frame := func(idx string) []byte {
		return []byte(`{"e":"markPriceUpdate","E":1,"s":"SKLUSDT","p":"0.05","i":"` + idx + `","r":"0.00005","T":1790403600000}`)
	}
	count := func() int {
		n := 0
		for _, fs := range got {
			for _, f := range fs {
				if f.Kind == "index_price" {
					n++
				}
			}
		}
		return n
	}
	b.handleMarkPrice(context.Background(), frame("4361.25")) // t=0：首帧落
	if n := count(); n != 1 {
		t.Fatalf("首帧 index want 1, got %d", n)
	}
	nowMu.Add(5 * time.Minute.Milliseconds())
	b.handleMarkPrice(context.Background(), frame("4362.00")) // t=5min 值变：窗内不写
	if n := count(); n != 1 {
		t.Fatalf("10min 内值变帧不得写 index, got %d", n)
	}
	nowMu.Add(6 * time.Minute.Milliseconds()) // t=11min：过 10min 落
	b.handleMarkPrice(context.Background(), frame("4363.00"))
	if n := count(); n != 2 {
		t.Fatalf("过 10min 应写第二笔 index, got %d", n)
	}
	if last := got[len(got)-1]; last[len(last)-1].Value != 4363.0 {
		t.Fatalf("第二笔 index 值 want 4363.0, got %v", last[len(last)-1].Value)
	}
}
