// cex_executor_maker_wait_test.go —— D-018 R1 验收锚：AwaitPostOnlyFill 事件驱动——
// 等待期间零 REST 查单调用（httptest 计数）；WS 事件帧（cache PutOrder）唤醒即刻终判。
package exec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/collect/exchange"
)

// TestAwaitPostOnlyFill_EventDrivenZeroREST R1 验收①：等待期间零 REST——
// 事件到达即刻终判（快照闭包直读），HTTP 服务端零命中；未接线 lookup 裸等超时亦零 REST。
func TestAwaitPostOnlyFill_EventDrivenZeroREST(t *testing.T) {
	var httpHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&httpHits, 1) // 任何 REST 触达（查单/fee/spec 回落）都会命中
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":"0","data":[]}`))
	}))
	defer srv.Close()

	cache := exchange.NewOrderEventCache()
	e := &CEXExecutor{venue: "okx", venueType: "perp", baseURL: srv.URL}
	e.SetOKXOrderLookup(func(ordID string) (OKXOrderSnap, bool) {
		ev, ok := cache.PeekOrder("gate:perp", ordID)
		return OKXOrderSnap{State: ev.State, AccFillQty: ev.AccFillQty, AvgPx: ev.AvgPx, FeeUSD: ev.FeeUSD}, ok
	})
	e.SetOrderWake(cache.SubscribeEvents)

	o := Order{Symbol: "BTC-USDT", Side: "SELL", Qty: 1, Venue: "gate:perp"}
	cache.PutOrder("gate:perp", "po-ev", "", exchange.OrderEvent{State: "filled", AccFillQty: 1, AvgPx: 100, FeeUSD: 0.001, At: time.Now()})
	fill, filled, err := e.AwaitPostOnlyFill(context.Background(), o, "po-ev")
	if err != nil {
		t.Fatalf("AwaitPostOnlyFill: %v", err)
	}
	if !filled || fill.Qty != 1 || fill.FillKind != "maker" {
		t.Fatalf("事件帧应即刻终判 filled/maker，got filled=%v fill=%+v", filled, fill)
	}
	if n := atomic.LoadInt32(&httpHits); n != 0 {
		t.Fatalf("等待期 REST 命中=%d, want 0（事件驱动零查单）", n)
	}
}

// TestAwaitPostOnlyFill_WakeOnLateEvent 时序锚：挂单时快照 miss → 50ms 后 WS 帧
// （PutOrder）唤醒 → 无轮询节奏即刻返回；REST 零命中。
func TestAwaitPostOnlyFill_WakeOnLateEvent(t *testing.T) {
	var httpHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&httpHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cache := exchange.NewOrderEventCache()
	e := &CEXExecutor{venue: "gate", venueType: "perp", baseURL: srv.URL}
	e.SetGateOrderLookup(func(ordID string) (GateOrderSnap, bool) {
		ev, ok := cache.PeekOrder("gate:perp", ordID)
		return GateOrderSnap{State: ev.State, AccFillQty: ev.AccFillQty, AvgPx: ev.AvgPx, FeeUSD: ev.FeeUSD}, ok
	})
	e.SetOrderWake(cache.SubscribeEvents)

	o := Order{Symbol: "BTC_USDT", Side: "SELL", Qty: 1, Venue: "gate:perp"}
	go func() {
		time.Sleep(50 * time.Millisecond)
		cache.PutOrder("gate:perp", "po-late", "", exchange.OrderEvent{State: "filled", AccFillQty: 2, AvgPx: 99.9, At: time.Now()})
	}()
	start := time.Now()
	fill, filled, err := e.AwaitPostOnlyFill(context.Background(), o, "po-late")
	if err != nil {
		t.Fatalf("AwaitPostOnlyFill: %v", err)
	}
	elapsed := time.Since(start)
	if !filled || fill.Qty != 2 {
		t.Fatalf("晚到事件应唤醒终判 filled qty=2，got filled=%v fill=%+v", filled, fill)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("事件唤醒应在帧到达即刻返回，实际 %v（疑似残留轮询节奏）", elapsed)
	}
	if n := atomic.LoadInt32(&httpHits); n != 0 {
		t.Fatalf("等待期 REST 命中=%d, want 0", n)
	}
}

// TestAwaitPostOnlyFill_TimeoutNoREST 超时路径：无事件裸等到 ctx（=T）→ filled=false，
// 等待期零 REST（撤单终查的 REST 属 PostOnlyCancel，不在本函数计）。
func TestAwaitPostOnlyFill_TimeoutNoREST(t *testing.T) {
	var httpHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&httpHits, 1)
	}))
	defer srv.Close()

	cache := exchange.NewOrderEventCache()
	e := &CEXExecutor{venue: "okx", venueType: "perp", baseURL: srv.URL}
	e.SetOKXOrderLookup(func(ordID string) (OKXOrderSnap, bool) { return OKXOrderSnap{}, false })
	e.SetOrderWake(cache.SubscribeEvents)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, filled, err := e.AwaitPostOnlyFill(ctx, Order{Symbol: "BTC-USDT", Side: "SELL"}, "po-none")
	if err != nil {
		t.Fatalf("AwaitPostOnlyFill: %v", err)
	}
	if filled {
		t.Fatal("无事件超时应 filled=false（调用方撤单终查）")
	}
	if n := atomic.LoadInt32(&httpHits); n != 0 {
		t.Fatalf("超时等待期 REST 命中=%d, want 0", n)
	}
}
