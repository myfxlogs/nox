// cex_executor_maker_wait_test.go —— D-018 R1 验收锚：AwaitPostOnlyFill 事件驱动——
// 等待期间零 REST 查单调用（httptest 计数）；WS 事件帧（cache PutOrder）唤醒即刻终判。
package exec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/data/collect/exchange"
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

// TestPostOnlyCancel_FrameArrivalInstantFail D-018 阶段b 验收锚：秒撤帧到达即返回
// FAILED 不等 T——撤单请求后 WS canceled 帧（qty=0）写缓存，事件唤醒即刻终判；
// 查单 REST 零命中（撤单 POST 除外），全程亚秒（旧 200ms REST 循环=3s 窗）。
func TestPostOnlyCancel_FrameArrivalInstantFail(t *testing.T) {
	var statusHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK) // 撤单 POST 成功
			return
		}
		atomic.AddInt32(&statusHits, 1) // 任何查单 GET=REST 兜底被打到
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cache := exchange.NewOrderEventCache()
	e := &CEXExecutor{venue: "gate", venueType: "perp", baseURL: srv.URL, apiKey: "k", apiSecret: "s", client: &http.Client{Timeout: 5 * time.Second}}
	e.SetGateOrderLookup(func(ordID string) (GateOrderSnap, bool) {
		ev, ok := cache.PeekOrder("gate:perp", ordID)
		return GateOrderSnap{State: ev.State, AccFillQty: ev.AccFillQty, AvgPx: ev.AvgPx, FeeUSD: ev.FeeUSD}, ok
	})
	e.SetOrderWake(cache.SubscribeEvents)

	o := Order{Symbol: "BTC_USDT", Side: "SELL", Qty: 1, Venue: "gate:perp"}
	// 撤单请求后 30ms WS canceled 帧到达（快于任何 REST 节拍）。
	go func() {
		time.Sleep(30 * time.Millisecond)
		cache.PutOrder("gate:perp", "po-sec", "", exchange.OrderEvent{State: "canceled", AccFillQty: 0, At: time.Now()})
	}()
	start := time.Now()
	fill, err := e.PostOnlyCancel(context.Background(), o, "po-sec")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("PostOnlyCancel: %v", err)
	}
	if fill.Qty != 0 || fill.FillKind != "maker" {
		t.Fatalf("干净撤应 qty=0 maker，got %+v", fill)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("秒撤帧应即刻终判，实际 %v", elapsed)
	}
	if n := atomic.LoadInt32(&statusHits); n != 0 {
		t.Fatalf("帧到达路径查单 REST 命中=%d, want 0（事件唤醒+单次兜底未触发）", n)
	}
}

// TestPostOnlyCancel_NoEventSingleRESTFallback 兜底锚：无事件（orderWake 未接线）→
// 单次 REST 查单终判（canceled qty=0）返回——不循环。
func TestPostOnlyCancel_NoEventSingleRESTFallback(t *testing.T) {
	var statusHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete || strings.Contains(r.URL.Path, "cancel-order") {
			w.WriteHeader(http.StatusOK) // 撤单请求（gate DELETE / okx POST cancel-order）不算查单
			return
		}
		atomic.AddInt32(&statusHits, 1)
		_, _ = w.Write([]byte(`{"code":"0","data":[{"ordId":"po-x","state":"canceled","accFillSz":"0","avgPx":""}]}`))
	}))
	defer srv.Close()

	e := &CEXExecutor{venue: "okx", venueType: "perp", baseURL: srv.URL, apiKey: "k", apiSecret: "s", passphrase: "p", client: &http.Client{Timeout: 5 * time.Second}}
	// 无 SetOKXOrderLookup/SetOrderWake=未接线 → 直接单次 REST。
	fill, err := e.PostOnlyCancel(context.Background(), Order{Symbol: "BTC-USDT", Side: "SELL"}, "po-x")
	if err != nil {
		t.Fatalf("PostOnlyCancel: %v", err)
	}
	if fill.Qty != 0 {
		t.Fatalf("干净撤 qty=0, got %+v", fill)
	}
	if n := atomic.LoadInt32(&statusHits); n != 1 {
		t.Fatalf("兜底应恰一次查单 REST，got %d", n)
	}
}
