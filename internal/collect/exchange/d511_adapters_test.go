// d511_adapters_test.go —— D-511 R7 返修：适配层行为保真断言。
// ①gate Run 路径必须真用 g.spotURLs/g.futuresURLs（wsViaProxy 过的字段值）——R4 钉死，
// 防「字段是摆设、Run 回常量直连」回归（GFW RST 断供事故形态）；
// ②okx 差集补订：watchlist 新增符号 → 下个 alive tick 只补新符号帧（D-369 S2 语义保留）。
package exchange

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"arb/internal/bus"
)

// TestD511_GateRunUsesSpecURLs gate.Run 双连接落 spot/futures 两 fake server——
// URL 取自 g.spotURLs/g.futuresURLs（非常量直连），订阅 channel 各自正确。
func TestD511_GateRunUsesSpecURLs(t *testing.T) {
	spotFrames := make(chan []byte, 4)
	futFrames := make(chan []byte, 4)
	spotURL, spotUp := wsRunnerServer(t, false, spotFrames)
	futURL, futUp := wsRunnerServer(t, false, futFrames)

	g := NewGateWebSocket(Config{Symbols: []string{"BTC"}})
	g.spotURLs, g.futuresURLs = []string{spotURL}, []string{futURL} // 覆写=代理路由形态；Run 必须走字段值
	g.readIdle, g.pingEvery, g.reconnectBackoff = 300*time.Millisecond, 100*time.Millisecond, 50*time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx, func(bus.Quote) {})

	// spot 连接应收 spot.tickers 订阅帧；futures 收 futures.tickers。
	assertGateSub := func(ch chan []byte, want string) {
		t.Helper()
		select {
		case data := <-ch:
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("sub frame 非 JSON: %v", err)
			}
			if m["channel"] != want {
				t.Fatalf("channel=%v want %v", m["channel"], want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s 订阅帧未到（Run 未走字段 URL）", want)
		}
	}
	assertGateSub(spotFrames, "spot.tickers")
	assertGateSub(futFrames, "futures.tickers")
	time.Sleep(200 * time.Millisecond)
	if spotUp.Load() != 1 || futUp.Load() != 1 {
		t.Fatalf("upgrades=%d/%d want 1/1", spotUp.Load(), futUp.Load())
	}
}

// d511DynSyms 可变符号 provider（watchlist 新增模拟）。
type d511DynSyms struct {
	mu   sync.Mutex
	syms []string
}

func (d *d511DynSyms) Symbols() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.syms...)
}
func (d *d511DynSyms) add(s string) { d.mu.Lock(); d.syms = append(d.syms, s); d.mu.Unlock() }

// TestD511_OkxDeltaResubscribe okx watchlist 新增 ETH → alive tick 差集帧只含 ETH 三件
// （tickers spot+perp+funding-rate），BTC 不重复订；初连帧含 BTC 三件。
func TestD511_OkxDeltaResubscribe(t *testing.T) {
	frames := make(chan []byte, 32)
	url, _ := wsRunnerServer(t, false, frames)
	dyn := &d511DynSyms{}
	o := NewOKXWebSocket(Config{Symbols: []string{"BTC"}, SymProvider: dyn})
	o.wsURLs = []string{url}
	o.readIdle, o.pingEvery, o.reconnectBackoff = 400*time.Millisecond, 60*time.Millisecond, 40*time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go o.Run(ctx, func(bus.Quote) {})

	var first []byte
	select {
	case first = <-frames:
	case <-time.After(2 * time.Second):
		t.Fatal("初连订阅帧未到")
	}
	if !strings.Contains(string(first), "BTC-USDT") || !strings.Contains(string(first), "funding-rate") {
		t.Fatalf("初连帧缺 BTC args/funding-rate: %s", first)
	}

	dyn.add("ETH") // watchlist 新增 → 下个 tick 差集补订
	var delta []byte
	select {
	case delta = <-frames:
	case <-time.After(2 * time.Second):
		t.Fatal("差集补订帧未到")
	}
	s := string(delta)
	if !strings.Contains(s, "ETH-USDT") || strings.Contains(s, "BTC-USDT") {
		t.Fatalf("差集帧应只含 ETH args: %s", s)
	}
}
