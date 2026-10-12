package exchange

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arb/internal/infra/dephealth"

	"github.com/gorilla/websocket"
)

func depRow(key string) (dephealth.Row, bool) {
	for _, r := range dephealth.Snapshot() {
		if r.Key == key {
			return r, true
		}
	}
	return dephealth.Row{}, false
}

// TestD579_T2_WSDepMark wsrunner 标成败：dial 成功→Mark ok（healthy）；
// runWSOnceSt 返错→Mark fail（suspect 起计）。
func TestD579_T2_WSDepMark(t *testing.T) {
	// 成功面：真 ws 回显服务，拨号成功读循环前标 healthy。
	keyOK := "t2ok:ws-market"
	frames := make(chan []byte, 8)
	url, _ := wsRunnerServer(t, false, frames)
	spec := wsRunnerSpec(url, frames)
	spec.DepKey = keyOK
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunWS(ctx, spec)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if r, ok := depRow(keyOK); ok && r.State == dephealth.StateHealthy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial 成功应标 healthy: %+v", dephealth.Snapshot())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	// 失败面：不可达地址→RunWS 出错分支记 fail（runWSOnce 直调不记账——挂点在 RunWS 环）。
	keyFail := "t2fail:ws-market"
	bad := wsRunnerSpec("ws://127.0.0.1:1", frames)
	bad.DepKey = keyFail
	bad.ReconnectBackoff = 10 * time.Millisecond
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go RunWS(ctx2, bad)
	deadline = time.Now().Add(3 * time.Second)
	for {
		if r, ok := depRow(keyFail); ok && r.ConsecFails >= 1 {
			if r.State != dephealth.StateSuspect {
				t.Fatalf("连败未达阈应 suspect: %+v", r)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("失败应记账: %+v", dephealth.Snapshot())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel2()
}

func waitDepState(t *testing.T, key, want string, d time.Duration) dephealth.Row {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if r, ok := depRow(key); ok && r.State == want {
			return r
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("%s 未达 %s: %+v", key, want, dephealth.Snapshot())
	return dephealth.Row{}
}

func waitDepFail(t *testing.T, key string, d time.Duration) dephealth.Row {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if r, ok := depRow(key); ok && r.ConsecFails >= 1 {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s 失败未记账: %+v", key, dephealth.Snapshot())
	return dephealth.Row{}
}

// TestD579_T2b_GatePrivateDepMark gate 私有流：fake REST(uid)+fake WS 全订阅帧发出→healthy；
// dial 假返错→runOnce 返错→Run 记 fail。
func TestD579_T2b_GatePrivateDepMark(t *testing.T) {
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/futures/usdt/accounts") {
			fmt.Fprint(w, `{"user":42,"total":"100"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer rest.Close()
	wsSrv := newWSFakeServer(t)

	g := NewGatePrivateWS("K", "S", rest.URL+"/api/v4", "gate:perp", false, nil, nil, NewOrderEventCache(), NewPositionEventCache())
	g.rest = rest.Client()
	g.dial = func(ctx context.Context, _ string) (*websocket.Conn, error) {
		conn, _, err := (&websocket.Dialer{}).DialContext(ctx, wsURLFromHTTP(wsSrv.URL), nil)
		return conn, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)
	for i := 0; i < len(gatePrivateChannels); i++ {
		select {
		case <-wsSrv.got:
		case <-time.After(5 * time.Second):
			t.Fatal("no subscribe frame")
		}
	}
	waitDepState(t, "gate:ws-private", dephealth.StateHealthy, 3*time.Second)
	cancel()

	// 失败面：dial 假返错→Run 出错分支记账。
	g2 := NewGatePrivateWS("K", "S", rest.URL+"/api/v4", "gate:perp", false, nil, nil, NewOrderEventCache(), NewPositionEventCache())
	g2.rest = rest.Client()
	g2.dial = func(context.Context, string) (*websocket.Conn, error) { return nil, fmt.Errorf("dial refused") }
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go g2.Run(ctx2)
	waitDepFail(t, "gate:ws-private", 3*time.Second)
	cancel2()
}

// TestD579_T2b_BinanceUserDepMark binance 用户流：listenKey+dial fake 成→healthy；dial 错→fail。
func TestD579_T2b_BinanceUserDepMark(t *testing.T) {
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/listenKey") {
			fmt.Fprint(w, `{"listenKey":"K1"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer rest.Close()
	wsSrv := newWSFakeServer(t)

	b := NewBinanceUserWS("K", "S", rest.URL, "perp", "binance:perp", false, nil, nil, NewOrderEventCache(), NewPositionEventCache(), BinanceUserHooks{})
	b.rest = rest.Client()
	b.dial = func(ctx context.Context, _ string) (*websocket.Conn, error) {
		conn, _, err := (&websocket.Dialer{}).DialContext(ctx, wsURLFromHTTP(wsSrv.URL), nil)
		return conn, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	waitDepState(t, "binance:ws-perp-user", dephealth.StateHealthy, 5*time.Second)
	cancel()

	b2 := NewBinanceUserWS("K", "S", rest.URL, "perp", "binance:perp", false, nil, nil, NewOrderEventCache(), NewPositionEventCache(), BinanceUserHooks{})
	b2.rest = rest.Client()
	b2.dial = func(context.Context, string) (*websocket.Conn, error) { return nil, fmt.Errorf("dial refused") }
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go b2.Run(ctx2)
	waitDepFail(t, "binance:ws-perp-user", 3*time.Second)
	cancel2()
}

// TestD579_T2b_OKXPrivateDepMark okx 私有流：就绪点=subscribe 事件帧（非裸 TCP）——
// fake server 收 login 回 login 事件、收 subscribe 回 subscribe 事件→healthy；dial 错→fail。
func TestD579_T2b_OKXPrivateDepMark(t *testing.T) {
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var m map[string]any
			if err := conn.ReadJSON(&m); err != nil {
				return
			}
			switch m["op"] {
			case "login":
				_ = conn.WriteJSON(map[string]any{"event": "login", "code": "0"})
			case "subscribe":
				_ = conn.WriteJSON(map[string]any{"event": "subscribe", "arg": map[string]any{"channel": "account"}})
			}
		}
	}))
	defer srv.Close()

	o := NewOKXPrivateWS("K", "S", "P", false, nil, "okx:unified", nil, NewOrderEventCache(), NewPositionEventCache(), OKXPrivateHooks{})
	o.dialer = &websocket.Dialer{}
	o.wsURLs = []string{wsURLFromHTTP(srv.URL)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go o.Run(ctx)
	waitDepState(t, "okx:ws-private", dephealth.StateHealthy, 5*time.Second)
	cancel()

	o2 := NewOKXPrivateWS("K", "S", "P", false, nil, "okx:unified", nil, NewOrderEventCache(), NewPositionEventCache(), OKXPrivateHooks{})
	o2.dialer = &websocket.Dialer{}
	o2.wsURLs = []string{"ws://127.0.0.1:1/x"}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go o2.Run(ctx2)
	waitDepFail(t, "okx:ws-private", 5*time.Second)
	cancel2()
}
