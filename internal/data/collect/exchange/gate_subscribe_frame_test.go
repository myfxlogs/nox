// d450_ws_test.go —— D-450 S7：gate/binance 私有 WS + 事件缓存层测试。
// fake WS server（httptest+gorilla upgrade）+ fake REST server；表驱动；先红后绿锚见 S7 验收。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nox/internal/data/venue"
	"nox/internal/store/pgstore"

	"github.com/gorilla/websocket"
)

// ---------- fakes ----------

type fakeSnapSink struct {
	mu   chan struct{}
	lk   sync.Mutex
	rows []pgstore.VenueAccountSnapshot
}

func newFakeSnapSink() *fakeSnapSink { return &fakeSnapSink{mu: make(chan struct{}, 64)} }

func (f *fakeSnapSink) InsertVenueAccountSnapshots(_ context.Context, snaps []pgstore.VenueAccountSnapshot) error {
	f.lk.Lock()
	f.rows = append(f.rows, snaps...)
	f.lk.Unlock()
	select {
	case f.mu <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeSnapSink) snapshot() []pgstore.VenueAccountSnapshot {
	f.lk.Lock()
	defer f.lk.Unlock()
	return append([]pgstore.VenueAccountSnapshot(nil), f.rows...)
}

type fakeEventSink struct {
	ch       chan string
	lk       sync.Mutex
	rows     []string
	payloads []string // D-569 包B A13：载荷实录（margin_call 全帧断言）
}

func newFakeEventSink() *fakeEventSink { return &fakeEventSink{ch: make(chan string, 64)} }

func (f *fakeEventSink) InsertCEXPrivateEvent(_ context.Context, venue, venueType, channel, key string, payload []byte) error {
	f.lk.Lock()
	f.rows = append(f.rows, fmt.Sprintf("%s:%s:%s:%s", venue, venueType, channel, key))
	f.payloads = append(f.payloads, string(payload))
	f.lk.Unlock()
	select {
	case f.ch <- channel:
	default:
	}
	return nil
}

func (f *fakeEventSink) snapshot() []string {
	f.lk.Lock()
	defer f.lk.Unlock()
	return append([]string(nil), f.rows...)
}

type panicRESTQuerier struct{ calls *atomic.Int32 }

func (p panicRESTQuerier) QueryOrderStatus(context.Context, string, string, string) (string, float64, float64, float64, error) {
	p.calls.Add(1)
	return "filled", 1, 2, 3, nil
}

// wsFakeServer 起 WS 假服务端：升级后回放 subscribe 帧+可选推一帧事件。
type wsFakeServer struct {
	*httptest.Server
	got   chan map[string]any
	pushC chan json.RawMessage
	conn  atomic.Pointer[websocket.Conn]
}

func (s *wsFakeServer) kill() {
	if c := s.conn.Swap(nil); c != nil {
		_ = c.Close()
	}
}

func newWSFakeServer(t *testing.T) *wsFakeServer {
	t.Helper()
	s := &wsFakeServer{got: make(chan map[string]any, 16), pushC: make(chan json.RawMessage, 16)}
	up := websocket.Upgrader{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s.conn.Store(conn)
		defer conn.Close()
		go func() {
			for push := range s.pushC {
				_ = conn.WriteMessage(websocket.TextMessage, push)
			}
		}()
		for {
			var m map[string]any
			if err := conn.ReadJSON(&m); err != nil {
				return
			}
			s.got <- m
		}
	}))
	t.Cleanup(s.Server.Close)
	return s
}

func wsURLFromHTTP(u string) string { return "ws" + strings.TrimPrefix(u, "http") }

// ---------- gate ----------

// T-gate-1：订阅帧形制——channel/auth 三字段+签名 hex64；payload 按频道分派两形制
// （生产实证矩阵：balances=[uid]、positions/orders/usertrades=[uid,"!all"]）。
func TestGateSubscribeFrame(t *testing.T) {
	g := &GatePrivateWS{apiKey: "K", apiSecret: "S", now: func() time.Time { return time.Unix(1700000000, 0) }}
	f := g.subscribeFrame("futures.orders", "42")
	if f["channel"] != "futures.orders" || f["event"] != "subscribe" {
		t.Fatalf("frame head wrong: %v", f)
	}
	auth := f["auth"].(map[string]string)
	if auth["method"] != "api_key" || auth["KEY"] != "K" {
		t.Fatalf("auth wrong: %v", auth)
	}
	if len(auth["SIGN"]) != 128 {
		t.Fatalf("sign should be sha512 hex 128, got %d", len(auth["SIGN"]))
	}
	want := gateWSSign("S", "futures.orders", 1700000000)
	if auth["SIGN"] != want {
		t.Fatalf("sign mismatch")
	}
	// payload 两形制：balances 单参，其余双参。
	cases := map[string][]string{
		"futures.balances":   {"42"},
		"futures.positions":  {"42", "!all"},
		"futures.orders":     {"42", "!all"},
		"futures.usertrades": {"42", "!all"},
	}
	for ch, want := range cases {
		pl := g.subscribeFrame(ch, "42")["payload"].([]string)
		if len(pl) != len(want) || pl[0] != want[0] || (len(want) == 2 && pl[1] != want[1]) {
			t.Fatalf("%s payload=%v want %v", ch, pl, want)
		}
	}
}

// T-gate-2：WS URL 派生——base_url 去 /api/v4 + /v4/ws/usdt + scheme 换。
func TestGateWSURL(t *testing.T) {
	g := &GatePrivateWS{baseURL: "https://192.229.85.35:8443/api/v4"}
	if got := g.WSURL(); got != "wss://192.229.85.35:8443/v4/ws/usdt" {
		t.Fatalf("WSURL wrong: %s", got)
	}
	g.baseURL = "http://x:1/api/v4"
	if got := g.WSURL(); got != "ws://x:1/v4/ws/usdt" {
		t.Fatalf("WSURL http→ws wrong: %s", got)
	}
}

// T-gate-3：fake WS server 端到端——subscribe 帧到达服务端、update 事件→sink/缓存/审计。
func TestGateWSE2E(t *testing.T) {
	// REST fake：accounts 返回 uid=42；contracts 返回 quanto。
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/futures/usdt/accounts"):
			fmt.Fprint(w, `{"user":42,"total":"100"}`)
		case strings.Contains(r.URL.Path, "/futures/usdt/contracts/"):
			fmt.Fprint(w, `{"quanto_multiplier":"0.001"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer rest.Close()
	wsSrv := newWSFakeServer(t)

	g := NewGatePrivateWS("K", "S", rest.URL+"/api/v4", "gate:perp", false, nil, nil, NewOrderEventCache(), NewPositionEventCache())
	snaps := newFakeSnapSink()
	evts := newFakeEventSink()
	g.sink = snaps
	g.events = evts
	g.rest = rest.Client()
	g.dial = func(ctx context.Context, wsURL string) (*websocket.Conn, error) {
		// 真实路径断言：WSURL 派生正确（/v4/ws/usdt）。
		u, err := url.Parse(wsURL)
		if err != nil || u.Path != "/v4/ws/usdt" {
			return nil, fmt.Errorf("ws url wrong: %s", wsURL)
		}
		d := websocket.Dialer{}
		conn, _, err := d.DialContext(ctx, wsURLFromHTTP(wsSrv.URL), nil)
		return conn, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go g.Run(ctx)

	// 等订阅帧全量到服务端（D-570 包C：频道面 4→9，按 len(gatePrivateChannels) 等）。
	seen := map[string]bool{}
	for i := 0; i < len(gatePrivateChannels); i++ {
		select {
		case m := <-wsSrv.got:
			seen[m["channel"].(string)] = true
		case <-ctx.Done():
			t.Fatalf("timeout waiting subscribes, got %v", seen)
		}
	}
	for _, ch := range gatePrivateChannels {
		if !seen[ch] {
			t.Fatalf("missing subscribe %s", ch)
		}
	}
	// 推 balances + orders 事件。
	wsSrv.pushC <- json.RawMessage(`{"channel":"futures.balances","event":"update","result":[{"currency":"USDT","balance":"1234.5"}]}`)
	wsSrv.pushC <- json.RawMessage(`{"channel":"futures.orders","event":"update","result":[{"id":777,"text":"arb-x","contract":"BTC_USDT","status":"finished","finish_as":"filled","size":10,"left":0,"fill_price":"65000"}]}`)
	wsSrv.pushC <- json.RawMessage(`{"channel":"futures.positions","event":"update","result":[{"contract":"BTC_USDT","size":5,"entry_price":"64000","margin":"320"}]}`)

	// 等 sink 行（balances 触发）。
	select {
	case <-snaps.mu:
	case <-ctx.Done():
		t.Fatal("no snapshot row")
	}
	if len(snaps.snapshot()) != 1 || snaps.snapshot()[0].Src != "gate_event" || snaps.snapshot()[0].EquityRaw != 1234.5 {
		t.Fatalf("snapshot row wrong: %+v", snaps.snapshot())
	}
	// 等审计行≥3。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(evts.snapshot()) < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	if len(evts.snapshot()) < 3 {
		t.Fatalf("audit rows=%v", evts.snapshot())
	}
	if evts.snapshot()[1] != "gate:perp:futures.orders:777" {
		t.Fatalf("audit order row wrong: %v", evts.rows[1])
	}
	// 订单缓存命中。
	if ev, ok := g.orderCache.get("gate:perp", "777", "", EventFreshTTL, g.now()); !ok || ev.State != "filled" || ev.AvgPx != 65000 {
		t.Fatalf("order cache miss/wrong: %+v", ev)
	}
	// quanto=0.001 → accFillQty=10*0.001=0.01。
	if ev, _ := g.orderCache.get("gate:perp", "777", "", EventFreshTTL, g.now()); ev.AccFillQty != 0.01 {
		t.Fatalf("accFillQty=%v want 0.01", ev.AccFillQty)
	}
	// 持仓缓存命中。
	if orders, ok := g.posCache.get("gate:perp", PositionFreshTTL, g.now()); !ok || len(orders) != 1 || orders[0].Side != "BUY" || orders[0].PosSide != "long" {
		t.Fatalf("pos cache wrong: %+v", orders)
	}
}

// ---------- binance ----------

// T-bnc-1：WS URL 三路穷举映射。
func TestBinanceWSURLMap(t *testing.T) {
	cases := []struct {
		base      string
		venueType string
		sim       bool
		want      string
	}{
		{"https://p:8443/fapi", "perp", false, "wss://p:8443/private/ws/KEY1"}, // D-517 S3：fstream /private 迁移
		{"https://p:8443/api", "spot", false, "wss://p:8443/spotws/ws/KEY1"},
	}
	for _, c := range cases {
		b := &BinanceUserWS{baseURL: c.base, venueType: c.venueType, simulated: c.sim}
		got, err := b.WSURL("KEY1")
		if err != nil || got != c.want {
			t.Fatalf("base=%s type=%s sim=%v → %s err=%v want %s", c.base, c.venueType, c.sim, got, err, c.want)
		}
	}
	// simulated=t fail-closed（/demofws 支路删除，绝不拨 demo 端点）。
	b := &BinanceUserWS{baseURL: "https://p:8443/demofapi/fapi", venueType: "perp", simulated: true}
	if got, err := b.WSURL("KEY1"); err == nil || got != "" {
		t.Fatalf("simulated WSURL = %q err=%v want fail-closed error", got, err)
	}
}

// T-bnc-2：listenKey 端点（审计 R1：perp=base+/v1/listenKey 不双拼 /fapi；spot=/api/v3/userDataStream）。
func TestBinanceListenKeyEndpoint(t *testing.T) {
	b := &BinanceUserWS{baseURL: "https://p:8443/fapi", venueType: "perp"}
	if got := b.listenKeyEndpoint(); got != "https://p:8443/fapi/v1/listenKey" {
		t.Fatalf("perp endpoint wrong: %s", got)
	}
	// 包B S7 顺带清理：spot REST listenKey 死分支已删（spot 恒走 ws-api，runOnceSpot 路径）。
}

// T-bnc-3：端到端——REST 发 key→WS dial URL 正确→ORDER_TRADE_UPDATE→缓存+审计→断线重取新 key。
// CI 历史红清账①：真型=测试自竞态非外网 flake（退回㊹订正——env 门曾立后撤，
// reconnectEvery 注入 ~100ms 消竞态确定性修）。
func TestBinanceUserWSE2E(t *testing.T) {
	// 退回㊹订正：env 门撤（fake server 零外网——真型=测试自竞态非外网 flake；
	// reconnectEvery 注入 ~100ms 消竞态确定性修）
	var posts, puts atomic.Int32
	keyN := atomic.Int32{}
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/listenKey"):
			posts.Add(1)
			fmt.Fprintf(w, `{"listenKey":"K%d"}`, keyN.Add(1))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/v1/listenKey"):
			puts.Add(1)
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer rest.Close()
	wsSrv := newWSFakeServer(t)
	dialed := make(chan string, 8)

	b := NewBinanceUserWS("K", "S", rest.URL, "perp", "binance:perp", false, nil, nil, NewOrderEventCache(), NewPositionEventCache(), BinanceUserHooks{})
	evts := newFakeEventSink()
	snaps := newFakeSnapSink()
	b.events, b.sink = evts, snaps
	b.rest = rest.Client()
	b.renewEvery = 150 * time.Millisecond     // 注入缩短续租周期
	b.reconnectEvery = 100 * time.Millisecond // 退回㊹：重连节拍缩短消竞态（确定性修非遮错）
	b.dial = func(ctx context.Context, wsURL string) (*websocket.Conn, error) {
		dialed <- wsURL
		d := websocket.Dialer{}
		conn, _, err := d.DialContext(ctx, wsURLFromHTTP(wsSrv.URL), nil)
		return conn, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go b.Run(ctx)

	// dial URL=ws://host/ws/K1。
	select {
	case u := <-dialed:
		if !strings.HasSuffix(u, "/ws/K1") {
			t.Fatalf("dial url wrong: %s", u)
		}
	case <-ctx.Done():
		t.Fatal("no dial")
	}
	// 推订单事件 → 缓存 + 审计。
	wsSrv.pushC <- json.RawMessage(`{"e":"ORDER_TRADE_UPDATE","o":{"i":888,"c":"arb-1","X":"FILLED","z":"0.5","ap":"65000","n":"0.01","N":"USDT"}}`)
	wsSrv.pushC <- json.RawMessage(`{"e":"ACCOUNT_UPDATE","a":{"B":[{"a":"USDT","wb":"999"}],"P":[{"s":"BTCUSDT","pa":"0.5","ep":"64000","iw":"300","ps":"LONG"}]}}`)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(evts.snapshot()) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if len(evts.snapshot()) < 2 {
		t.Fatalf("audit rows=%v", evts.snapshot())
	}
	if ev, ok := b.orderCache.get("binance:perp", "888", "", EventFreshTTL, b.now()); !ok || ev.State != "filled" || ev.AccFillQty != 0.5 || ev.AvgPx != 65000 || ev.FeeUSD != 0.01 {
		t.Fatalf("order cache wrong: %+v", ev)
	}
	if orders, ok := b.posCache.get("binance:perp", PositionFreshTTL, b.now()); !ok || len(orders) != 1 || orders[0].InitialMargin != 300 {
		t.Fatalf("pos cache wrong: %+v", orders)
	}
	if len(snaps.snapshot()) != 1 || snaps.snapshot()[0].Src != "binance_event" || snaps.snapshot()[0].EquityRaw != 999 {
		t.Fatalf("snapshot wrong: %+v", snaps.snapshot())
	}
	// 续租 PUT 发生（renewEvery=150ms）。
	time.Sleep(400 * time.Millisecond)
	if puts.Load() == 0 {
		t.Fatal("no renew PUT")
	}
	// 断流 → 重取 key（POST 计数增）。
	wsSrv.kill()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && posts.Load() < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	if posts.Load() < 2 {
		t.Fatalf("reconnect did not reacquire key, posts=%d", posts.Load())
	}
	select {
	case u := <-dialed:
		if !strings.HasSuffix(u, "/ws/K2") {
			t.Fatalf("reconnect dial url wrong: %s", u)
		}
	case <-ctx.Done():
		t.Fatal("no redial")
	}
}

// ---------- 缓存层 ----------

// T-cache-1：fresh 命中不碰 REST；stale/miss 回落 REST。
func TestCachedOrderStatusQuerier(t *testing.T) {
	calls := &atomic.Int32{}
	rest := panicRESTQuerier{calls: calls}
	cache := NewOrderEventCache()
	q := NewCachedOrderStatusQuerier("gate:perp", rest, cache)
	now := time.Now()
	q.now = func() time.Time { return now }

	// miss → REST。
	st, _, _, _, err := q.QueryOrderStatus(context.Background(), "1", "", "BTC_USDT")
	if err != nil || st != "filled" || calls.Load() != 1 {
		t.Fatalf("miss should hit REST, calls=%d err=%v", calls.Load(), err)
	}
	// 写 fresh 事件 → 不碰 REST。
	cache.PutOrder("gate:perp", "1", "c1", OrderEvent{State: "partial", AccFillQty: 0.5, At: now})
	st, qty, _, _, _ := q.QueryOrderStatus(context.Background(), "1", "", "BTC_USDT")
	if st != "partial" || qty != 0.5 || calls.Load() != 1 {
		t.Fatalf("fresh should bypass REST, st=%s calls=%d", st, calls.Load())
	}
	// clOrdID 键同样命中。
	st, _, _, _, _ = q.QueryOrderStatus(context.Background(), "", "c1", "BTC_USDT")
	if st != "partial" || calls.Load() != 1 {
		t.Fatalf("clOrdID key should hit cache")
	}
	// 事件过期 → 回落 REST。
	q.now = func() time.Time { return now.Add(6 * time.Second) }
	st, _, _, _, _ = q.QueryOrderStatus(context.Background(), "1", "", "BTC_USDT")
	if st != "filled" || calls.Load() != 2 {
		t.Fatalf("stale should fall back REST, st=%s calls=%d", st, calls.Load())
	}
	// 撞键隔离：别的 venueKey 不命中。
	if _, ok := cache.get("binance:perp", "1", "", EventFreshTTL, now); ok {
		t.Fatal("venueKey isolation broken")
	}
}

// T-cache-2：持仓全量快照 fresh→直返副本；stale→REST。
func TestCachedVenueQuerier(t *testing.T) {
	calls := &atomic.Int32{}
	rest := fakeVenueQuerier{calls: calls, vk: "gate:perp"}
	cache := NewPositionEventCache()
	q := NewCachedVenueQuerier(rest, cache)
	now := time.Now()
	q.now = func() time.Time { return now }

	orders, err := q.QueryOpenedOrders(context.Background())
	if err != nil || len(orders) != 1 || calls.Load() != 1 {
		t.Fatalf("miss→REST, calls=%d", calls.Load())
	}
	cache.PutPositions("gate:perp", []venue.Order{{Symbol: "X", Side: "BUY"}}, now)
	orders, _ = q.QueryOpenedOrders(context.Background())
	if len(orders) != 1 || orders[0].Symbol != "X" || calls.Load() != 1 {
		t.Fatalf("fresh should bypass REST")
	}
	// 缓存替换语义：再次 Put 全量替换。
	cache.PutPositions("gate:perp", nil, now)
	orders, _ = q.QueryOpenedOrders(context.Background())
	if len(orders) != 0 || calls.Load() != 1 {
		t.Fatalf("empty snapshot should replace")
	}
	// stale → REST。
	q.now = func() time.Time { return now.Add(11 * time.Second) }
	if _, _ = q.QueryOpenedOrders(context.Background()); calls.Load() != 2 {
		t.Fatalf("stale→REST, calls=%d", calls.Load())
	}
	// VenueKey 透传。
	if q.VenueKey() != "gate:perp" {
		t.Fatalf("VenueKey=%s", q.VenueKey())
	}
}

type fakeVenueQuerier struct {
	calls *atomic.Int32
	vk    string
}

func (f fakeVenueQuerier) VenueKey() string { return f.vk }
func (f fakeVenueQuerier) QueryOpenedOrders(context.Context) ([]venue.Order, error) {
	f.calls.Add(1)
	return []venue.Order{{Symbol: "REST"}}, nil
}

// 编译期断言：包装器实现目标接口。
var (
	_ OrderStatusQuerier = (*CachedOrderStatusQuerier)(nil)
	_ venue.Querier      = (*CachedVenueQuerier)(nil)
)

// ---------- 返修 R1：demo 流禁写快照 ----------

// T-R1a：gate simulated=true → balances 帧不落快照（对齐 REST IsRealAccount 只收 real），审计行仍写。
func TestGateDemoSnapshotGuard(t *testing.T) {
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/futures/usdt/accounts") {
			fmt.Fprint(w, `{"user":7,"total":"50"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer rest.Close()
	wsSrv := newWSFakeServer(t)

	g := NewGatePrivateWS("K", "S", rest.URL+"/api/v4", "gate:perp#9", true, nil, nil, NewOrderEventCache(), NewPositionEventCache())
	snaps := newFakeSnapSink()
	evts := newFakeEventSink()
	g.sink, g.events, g.rest = snaps, evts, rest.Client()
	g.dial = func(ctx context.Context, _ string) (*websocket.Conn, error) {
		conn, _, err := (&websocket.Dialer{}).DialContext(ctx, wsURLFromHTTP(wsSrv.URL), nil)
		return conn, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go g.Run(ctx)
	select {
	case <-wsSrv.got:
	case <-ctx.Done():
		t.Fatal("no subscribe")
	}
	wsSrv.pushC <- json.RawMessage(`{"channel":"futures.balances","event":"update","result":[{"currency":"USDT","balance":"888"}]}`)
	// 等审计行=帧已处理，然后断言快照为空。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(evts.snapshot()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if len(evts.snapshot()) == 0 {
		t.Fatal("frame not processed (no audit row)")
	}
	time.Sleep(100 * time.Millisecond)
	if got := snaps.snapshot(); len(got) != 0 {
		t.Fatalf("demo stream wrote snapshots: %+v", got)
	}
}

// T-R1b：binance simulated=true → D-516 起 fail-closed：Run 不取 key 不拨连接
// （demo 端点支路删除；事件层 !simulated 快照守卫保留为防御位）。
func TestBinanceDemoSnapshotGuard(t *testing.T) {
	var posts atomic.Int32
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/listenKey") {
			posts.Add(1)
			fmt.Fprint(w, `{"listenKey":"D1"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer rest.Close()
	var dials atomic.Int32
	b := NewBinanceUserWS("K", "S", rest.URL, "perp", "binance:perp#25", true, nil, nil, NewOrderEventCache(), NewPositionEventCache(), BinanceUserHooks{})
	b.dial = func(ctx context.Context, _ string) (*websocket.Conn, error) {
		dials.Add(1)
		return nil, fmt.Errorf("should not dial")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	b.Run(ctx)
	if posts.Load() != 0 || dials.Load() != 0 {
		t.Fatalf("simulated config dialed: listenKey posts=%d dials=%d", posts.Load(), dials.Load())
	}
}

// ---------- 返修 R2：listenKeyExpired 关连重取 ----------

// T-R2：收到 listenKeyExpired 帧 → 关连接 → runOnce 返回 → Run 重取新 key 重拨。
func TestBinanceListenKeyExpiredReconnect(t *testing.T) {
	var posts atomic.Int32
	keyN := atomic.Int32{}
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/listenKey") {
			posts.Add(1)
			fmt.Fprintf(w, `{"listenKey":"K%d"}`, keyN.Add(1))
			return
		}
		http.NotFound(w, r)
	}))
	defer rest.Close()
	wsSrv := newWSFakeServer(t)
	dialed := make(chan string, 8)

	b := NewBinanceUserWS("K", "S", rest.URL, "perp", "binance:perp", false, nil, nil, NewOrderEventCache(), NewPositionEventCache(), BinanceUserHooks{})
	b.rest = rest.Client()
	b.dial = func(ctx context.Context, wsURL string) (*websocket.Conn, error) {
		dialed <- wsURL
		conn, _, err := (&websocket.Dialer{}).DialContext(ctx, wsURLFromHTTP(wsSrv.URL), nil)
		return conn, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go b.Run(ctx)

	select {
	case u := <-dialed:
		if !strings.HasSuffix(u, "/ws/K1") {
			t.Fatalf("dial url wrong: %s", u)
		}
	case <-ctx.Done():
		t.Fatal("no dial")
	}
	// 推 listenKeyExpired → 关连 → 重取 key。
	wsSrv.pushC <- json.RawMessage(`{"e":"listenKeyExpired"}`)
	// 重连退避=5s——deadline 放宽避免与退避擦线 flake。
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && posts.Load() < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	if posts.Load() < 2 {
		t.Fatalf("listenKeyExpired did not trigger reacquire, posts=%d", posts.Load())
	}
	select {
	case u := <-dialed:
		if !strings.HasSuffix(u, "/ws/K2") {
			t.Fatalf("redial url wrong: %s", u)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("no redial")
	}
}
