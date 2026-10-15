// d459_spot_ws_test.go —— D-459：binance spot 用户流 ws-api 支路测试。
// fake ws-api 控制服务器（应答 start/ping）+ fake 流服务器（推 spot 事件帧）。
// S0 实证形制：start params={apiKey} 免签名；ping/stop params={apiKey,listenKey}。
package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
)

// wsapiFakeServer 起 ws-api 假服务端：对 start 回 listenKey、ping/stop 回空 result，
// 非 200 路径由 failCode 注入。请求帧逐条进 reqs。
type wsapiFakeServer struct {
	*httptest.Server
	reqs     chan map[string]any
	starts   *atomic.Int32
	failCode int // !=0 时 start 回该 status
	conn     atomic.Pointer[websocket.Conn]
}

func newWsapiFakeServer(t *testing.T) *wsapiFakeServer {
	t.Helper()
	s := &wsapiFakeServer{reqs: make(chan map[string]any, 32), starts: &atomic.Int32{}}
	up := websocket.Upgrader{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s.conn.Store(conn)
		defer conn.Close()
		for {
			var m map[string]any
			if err := conn.ReadJSON(&m); err != nil {
				return
			}
			s.reqs <- m
			method, _ := m["method"].(string)
			switch method {
			case "userDataStream.start":
				s.starts.Add(1)
				if s.failCode != 0 {
					_ = conn.WriteJSON(map[string]any{"id": m["id"], "status": s.failCode,
						"error": map[string]any{"code": -1, "msg": "fake fail"}})
					continue
				}
				_ = conn.WriteJSON(map[string]any{"id": m["id"], "status": 200,
					"result": map[string]any{"listenKey": "LK"}})
			default: // ping/stop
				_ = conn.WriteJSON(map[string]any{"id": m["id"], "status": 200, "result": map[string]any{}})
			}
		}
	}))
	t.Cleanup(s.Server.Close)
	return s
}

// TestBinanceSpotWsapiE2E ws-api start→流拨号→spot 事件→续租 ping→断流重连全链。
func TestBinanceSpotWsapiE2E(t *testing.T) {
	wsapi := newWsapiFakeServer(t)
	stream := newWSFakeServer(t)
	dialed := make(chan string, 8)

	b := NewBinanceUserWS("K", "S", "https://unused.invalid", "spot", "binance:spot", false, nil, nil, NewOrderEventCache(), NewPositionEventCache(), BinanceUserHooks{})
	evts := newFakeEventSink()
	snaps := newFakeSnapSink()
	b.events, b.sink = evts, snaps
	b.renewEvery = 150 * time.Millisecond
	b.dial = func(ctx context.Context, wsURL string) (*websocket.Conn, error) {
		dialed <- wsURL
		d := websocket.Dialer{}
		target := stream.URL
		if strings.Contains(wsURL, "/wsapi/") {
			target = wsapi.URL
		}
		conn, _, err := d.DialContext(ctx, wsURLFromHTTP(target), nil)
		return conn, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go b.Run(ctx)

	// 1) ws-api 控制连接 + start 帧 params 只含 apiKey（实证免签名形制）。
	var startReq map[string]any
	select {
	case u := <-dialed:
		if !strings.HasSuffix(u, "/wsapi/ws-api/v3") {
			t.Fatalf("control dial url wrong: %s", u)
		}
	case <-ctx.Done():
		t.Fatal("no ws-api dial")
	}
	select {
	case startReq = <-wsapi.reqs:
	case <-ctx.Done():
		t.Fatal("no start frame")
	}
	params, _ := startReq["params"].(map[string]any)
	if startReq["method"] != "userDataStream.start" || len(params) != 1 || params["apiKey"] != "K" {
		t.Fatalf("start frame wrong: %v", startReq)
	}
	// 2) 流连接拨 /spotws/ws/LK。
	select {
	case u := <-dialed:
		if !strings.HasSuffix(u, "/spotws/ws/LK") {
			t.Fatalf("stream dial url wrong: %s", u)
		}
	case <-ctx.Done():
		t.Fatal("no stream dial")
	}
	// 3) spot 事件：executionReport→订单缓存+审计；outboundAccountPosition→USDT f+l 快照。
	stream.pushC <- json.RawMessage(`{"e":"executionReport","i":777,"c":"arb-9","X":"FILLED","z":"1.5","L":"2500","n":"0.02","N":"USDT"}`)
	stream.pushC <- json.RawMessage(`{"e":"outboundAccountPosition","B":[{"a":"USDT","f":"800","l":"50"},{"a":"BTC","f":"1","l":"0"}]}`)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(evts.snapshot()) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if len(evts.snapshot()) < 2 {
		t.Fatalf("audit rows=%v", evts.snapshot())
	}
	if ev, ok := b.orderCache.get("binance:spot", "777", "", EventFreshTTL, b.now()); !ok || ev.State != "filled" || ev.AccFillQty != 1.5 || ev.AvgPx != 2500 || ev.FeeUSD != 0.02 {
		t.Fatalf("order cache wrong: %+v", ev)
	}
	// D-528：分币行随帧同写——USDT 850 + BTC 1（原生量）两行。
	if rows := snaps.snapshot(); len(rows) != 2 {
		t.Fatalf("snapshot wrong: %+v", rows)
	} else {
		per := map[string]pgstore.VenueAccountSnapshot{}
		for _, r := range rows {
			per[r.Currency] = r
		}
		if per["USDT"].EquityRaw != 850 || per["USDT"].VenueType != "spot" ||
			per["BTC"].EquityRaw != 1 || per["BTC"].FreeMarginRaw != 1 {
			t.Fatalf("snapshot wrong: %+v", rows)
		}
	}
	// 全资产落活缓存——BTC 行同帧入资产级面（USDT 池聚合径不变）。
	if ab, ok := bus.SpotAssets.Get("binance:spot", "BTC"); !ok || ab.Free != 1 {
		t.Fatalf("asset ledger BTC wrong: %+v", ab)
	}
	if ab, ok := bus.SpotAssets.Get("binance:spot", "USDT"); !ok || ab.Free != 800 || ab.Locked != 50 {
		t.Fatalf("asset ledger USDT wrong: %+v", ab)
	}
	// 4) 续租 ping：params={apiKey,listenKey} 两参（实证形制）。
	deadline = time.Now().Add(2 * time.Second)
	var pingReq map[string]any
	for time.Now().Before(deadline) {
		select {
		case m := <-wsapi.reqs:
			if m["method"] == "userDataStream.ping" {
				pingReq = m
			}
		default:
			time.Sleep(10 * time.Millisecond)
		}
		if pingReq != nil {
			break
		}
	}
	if pingReq == nil {
		t.Fatal("no ping frame")
	}
	pp, _ := pingReq["params"].(map[string]any)
	if len(pp) != 2 || pp["apiKey"] != "K" || pp["listenKey"] != "LK" {
		t.Fatalf("ping frame wrong: %v", pingReq)
	}
	// 5) 断流 → 重连重取 key（starts 增 + 重拨 ws-api）。
	stream.kill()
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && wsapi.starts.Load() < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	if wsapi.starts.Load() < 2 {
		t.Fatalf("reconnect did not reacquire key, starts=%d", wsapi.starts.Load())
	}
}

// TestBinanceSpotWsapiStartFail start 被拒 → runOnce 返回错误 → Run 退避后重试（REST 兜底外的失败可见性）。
func TestBinanceSpotWsapiStartFail(t *testing.T) {
	wsapi := newWsapiFakeServer(t)
	wsapi.failCode = 400
	b := NewBinanceUserWS("K", "S", "https://unused.invalid", "spot", "binance:spot", false, nil, nil, NewOrderEventCache(), NewPositionEventCache(), BinanceUserHooks{})
	b.dial = func(ctx context.Context, wsURL string) (*websocket.Conn, error) {
		d := websocket.Dialer{}
		conn, _, err := d.DialContext(ctx, wsURLFromHTTP(wsapi.URL), nil)
		return conn, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go b.Run(ctx)
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) && wsapi.starts.Load() < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	if wsapi.starts.Load() < 2 {
		t.Fatalf("start failure did not retry, starts=%d", wsapi.starts.Load())
	}
}

// TestBinanceSpotWsapiURL ws-api URL 派生：baseURL 只取 scheme+host，路径固定 /wsapi/ws-api/v3。
func TestBinanceSpotWsapiURL(t *testing.T) {
	b := &BinanceUserWS{baseURL: "https://proxy.example:8443/api"}
	u, err := b.wsapiURL()
	if err != nil {
		t.Fatal(err)
	}
	if u != "wss://proxy.example:8443/wsapi/ws-api/v3" {
		t.Fatalf("wsapiURL=%s", u)
	}
}
