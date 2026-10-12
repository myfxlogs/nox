// reconnect_hook_test.go —— D-004 P1#6：CEX 私有 WS OnReconnect 钩子（重连就绪→对账 kick）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// gate：订阅帧发齐=就绪点 → hook 调一次；dial 失败 → 不调。
func TestGatePrivateWS_OnReconnect(t *testing.T) {
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/futures/usdt/accounts") {
			fmt.Fprint(w, `{"user":42,"total":"100"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer rest.Close()
	wsSrv := newWSFakeServer(t)
	var calls atomic.Int32
	g := NewGatePrivateWS("K", "S", rest.URL+"/api/v4", "gate:perp", false, nil, nil, NewOrderEventCache(), NewPositionEventCache())
	g.rest = rest.Client()
	g.dial = func(ctx context.Context, wsURL string) (*websocket.Conn, error) {
		d := websocket.Dialer{}
		conn, _, err := d.DialContext(ctx, wsURLFromHTTP(wsSrv.URL), nil)
		return conn, err
	}
	g.SetOnReconnect(func() { calls.Add(1) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go g.runOnce(ctx)
	waitCount(t, &calls, 1, 2*time.Second, "gate 首连就绪")
	// 断线 → 前一轮 runOnce 返回 err（Run 语义下会重连）→ 第二轮 runOnce 就绪再触发一次。
	wsSrv.kill()
	time.Sleep(50 * time.Millisecond) // 让读循环读到 EOF 返回
	go g.runOnce(ctx)
	waitCount(t, &calls, 2, 2*time.Second, "gate 重连就绪")
}

func TestGatePrivateWS_OnReconnectNotOnFailure(t *testing.T) {
	var calls atomic.Int32
	g := NewGatePrivateWS("K", "S", "http://127.0.0.1:0/api/v4", "gate:perp", false, nil, nil, NewOrderEventCache(), NewPositionEventCache())
	g.SetOnReconnect(func() { calls.Add(1) })
	if err := g.runOnce(context.Background()); err == nil {
		t.Fatal("want dial/fetchUID error")
	}
	if calls.Load() != 0 {
		t.Fatalf("calls=%d, want 0", calls.Load())
	}
}

// binance perp：listenKey+连接就绪 → hook；listenKey 失败 → 不调。
func TestBinanceUserWS_OnReconnect(t *testing.T) {
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/listenKey") {
			fmt.Fprint(w, `{"listenKey":"K1"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer rest.Close()
	wsSrv := newWSFakeServer(t)
	var calls atomic.Int32
	b := NewBinanceUserWS("K", "S", rest.URL, "perp", "binance:perp", false, nil, nil, NewOrderEventCache(), NewPositionEventCache(), BinanceUserHooks{})
	b.rest = rest.Client()
	b.dial = func(ctx context.Context, wsURL string) (*websocket.Conn, error) {
		d := websocket.Dialer{}
		conn, _, err := d.DialContext(ctx, wsURLFromHTTP(wsSrv.URL), nil)
		return conn, err
	}
	b.SetOnReconnect(func() { calls.Add(1) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go b.runOnce(ctx)
	waitCount(t, &calls, 1, 2*time.Second, "binance 首连就绪")
}

func TestBinanceUserWS_OnReconnectNotOnFailure(t *testing.T) {
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", 500)
	}))
	defer rest.Close()
	var calls atomic.Int32
	b := NewBinanceUserWS("K", "S", rest.URL, "perp", "binance:perp", false, nil, nil, NewOrderEventCache(), NewPositionEventCache(), BinanceUserHooks{})
	b.rest = rest.Client()
	b.SetOnReconnect(func() { calls.Add(1) })
	if err := b.runOnce(context.Background()); err == nil {
		t.Fatal("want listenKey error")
	}
	if calls.Load() != 0 {
		t.Fatalf("calls=%d, want 0", calls.Load())
	}
}

// okx：login → 订阅 ack 帧 → hook。
func TestOKXPrivateWS_OnReconnect(t *testing.T) {
	wsSrv := newWSFakeServer(t)
	var calls atomic.Int32
	o := NewOKXPrivateWS("K", "S", "P", false, nil, "okx:perp", nil, NewOrderEventCache(), NewPositionEventCache(), OKXPrivateHooks{})
	o.wsURLs = []string{wsURLFromHTTP(wsSrv.URL)}
	o.dialer = &websocket.Dialer{}
	o.SetOnReconnect(func() { calls.Add(1) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go o.runOnce(ctx)
	// 等 login 帧到 → 回 login ok；等 subscribe 帧 → 回 subscribe ack。
	select {
	case <-wsSrv.got:
	case <-ctx.Done():
		t.Fatal("no login frame")
	}
	wsSrv.pushC <- json.RawMessage(`{"event":"login","arg":{}}`)
	select {
	case <-wsSrv.got:
	case <-ctx.Done():
		t.Fatal("no subscribe frame")
	}
	wsSrv.pushC <- json.RawMessage(`{"event":"subscribe","arg":{"channel":"account"}}`)
	waitCount(t, &calls, 1, 2*time.Second, "okx 订阅就绪")
}

func TestOKXPrivateWS_OnReconnectNotOnFailure(t *testing.T) {
	var calls atomic.Int32
	o := NewOKXPrivateWS("K", "S", "P", false, nil, "okx:perp", nil, NewOrderEventCache(), NewPositionEventCache(), OKXPrivateHooks{})
	o.wsURLs = []string{"ws://127.0.0.1:0/none"}
	o.dialer = &websocket.Dialer{}
	o.SetOnReconnect(func() { calls.Add(1) })
	if err := o.runOnce(context.Background()); err == nil {
		t.Fatal("want dial error")
	}
	if calls.Load() != 0 {
		t.Fatalf("calls=%d, want 0", calls.Load())
	}
}

func waitCount(t *testing.T, c *atomic.Int32, want int32, d time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if c.Load() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: calls=%d, want >=%d", what, c.Load(), want)
}
