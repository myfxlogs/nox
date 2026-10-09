// d541_ws_test.go —— D-541 断言锚：S0 per-venue 止血开关 + S3 候选级 dialer
// 冷却降级（直连兜底位）。覆盖：gate 候选装配（兜底位裸 dialer+nil Header）、
// token 不泄原生域、连续僵尸杀→冷却→兜底接管→冷却到期自动回主。
package exchange

import (
	"arb/internal/httpx"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// wsDataServer 定期推数据帧的 fake server（兜底位接管后数据续流断言用）。
func wsDataServer(t *testing.T, every time.Duration) (string, *atomic.Int64) {
	t.Helper()
	n := &atomic.Int64{}
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		n.Add(1)
		defer conn.Close()
		for {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"e":"d"}`)); err != nil {
				return
			}
			time.Sleep(every)
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), n
}

// TestD541_S0_DirectMode ARB_GATE_WS_MODE=direct：强制原生直连——urls=native、
// 单候选零代理件（per-venue 止血，okx/binance 装配不动）。
func TestD541_S0_DirectMode(t *testing.T) {
	SetProxyEndpoints(ProxyEndpoints{CexWSBase: "wss://192.229.85.35:8443", GateWSMode: "direct"})
	httpx.Configure("", "tok-x", nil)
	defer func() { SetProxyEndpoints(ProxyEndpoints{}); httpx.Configure("", "", nil) }()
	g := NewGateWebSocket(Config{})
	if len(g.spotURLs) != 1 || g.spotURLs[0] != gateSpotWSURL ||
		len(g.futuresURLs) != 1 || g.futuresURLs[0] != gateFuturesWSURL {
		t.Fatalf("direct 模式 urls 未归原生: %v %v", g.spotURLs, g.futuresURLs)
	}
	if g.proxied || g.header != nil {
		t.Fatalf("direct 模式仍带代理件: proxied=%v hdr=%v", g.proxied, g.header)
	}
	c := g.candidates(g.spotURLs, gateSpotWSURL)
	if len(c) != 1 || c[0].URL != gateSpotWSURL || c[0].Header != nil {
		t.Fatalf("direct 候选表=%+v（应单 native 零头）", c)
	}
}

// TestD541_S0_DefaultProxy 缺省 proxy=现行——urls 代理路由+proxied 置位。
func TestD541_S0_DefaultProxy(t *testing.T) {
	SetProxyEndpoints(ProxyEndpoints{CexWSBase: "wss://192.229.85.35:8443"})
	httpx.Configure("", "tok-x", nil)
	defer func() { SetProxyEndpoints(ProxyEndpoints{}); httpx.Configure("", "", nil) }()
	g := NewGateWebSocket(Config{})
	if !g.proxied || g.spotURLs[0] != "wss://192.229.85.35:8443/gatespotws/ws/v4/" {
		t.Fatalf("缺省应代理装配: proxied=%v urls=%v", g.proxied, g.spotURLs)
	}
}

// TestD541_A1_FallbackCandBare 候选装配：代理模式候选表尾=native 兜底位，
// 裸 dialer+nil Header（X-Proxy-Token 不泄原生域——A1）。
func TestD541_A1_FallbackCandBare(t *testing.T) {
	SetProxyEndpoints(ProxyEndpoints{CexWSBase: "wss://192.229.85.35:8443"})
	httpx.Configure("", "tok-x", nil)
	defer func() { SetProxyEndpoints(ProxyEndpoints{}); httpx.Configure("", "", nil) }()
	g := NewGateWebSocket(Config{})
	c := g.candidates(g.spotURLs, gateSpotWSURL)
	if len(c) != 2 {
		t.Fatalf("代理模式候选=%d want 2（代理+直连兜底）", len(c))
	}
	if c[0].Header.Get("X-Proxy-Token") != "tok-x" || c[0].Dialer != g.dialer {
		t.Fatalf("主候选应代理件: %+v", c[0])
	}
	tail := c[len(c)-1]
	if tail.URL != gateSpotWSURL || tail.Header != nil || tail.Dialer == nil || tail.Dialer == g.dialer {
		t.Fatalf("兜底位应=native+裸dialer+nil头: %+v", tail)
	}
	// futures 同构。
	fc := g.candidates(g.futuresURLs, gateFuturesWSURL)
	if fc[len(fc)-1].URL != gateFuturesWSURL || fc[len(fc)-1].Header != nil {
		t.Fatalf("futures 兜底位错: %+v", fc[len(fc)-1])
	}
}

// TestD541_A1_NoTokenToNative 功能锚：主候选带 token 头拒连→落到直连兜底
// 候选 → 原生 server 握手零 X-Proxy-Token（R3 纪律实测）。
func TestD541_A1_NoTokenToNative(t *testing.T) {
	var got atomic.Value
	got.Store("")
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("X-Proxy-Token"))
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.ReadMessage()
	}))
	t.Cleanup(srv.Close)
	native := "ws" + strings.TrimPrefix(srv.URL, "http")
	hdr := http.Header{}
	hdr.Set("X-Proxy-Token", "tok-secret")
	spec := wsRunnerSpec(native, make(chan []byte, 8))
	spec.Candidates = []WSCandidate{
		{URL: "ws://127.0.0.1:1/dead", Header: hdr}, // 代理候选（token 头）
		{URL: native, Header: nil},                  // 直连兜底（nil 头）
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunWS(ctx, spec)
	time.Sleep(400 * time.Millisecond)
	cancel()
	if v := got.Load().(string); v != "" {
		t.Fatalf("token 泄给原生域: %q", v)
	}
}

// TestD541_A2_CooldownFallback 冷却降级+兜底接管+自动回主（A2/A3 僵尸演练）：
// 主候选 pong-活/数据死→连续 2 次数据看门狗杀→冷却→dial 落直连兜底候选
// 数据续流（断档<看门狗窗）→冷却到期 ping tick 判回主（主端恒优先）。
func TestD541_A2_CooldownFallback(t *testing.T) {
	primURL, primUp := wsRunnerServer(t, false, make(chan []byte, 8)) // pong 活/零数据帧=僵尸
	backURL, backUp := wsDataServer(t, 30*time.Millisecond)           // 兜底位出数
	dataFrames := make(chan []byte, 64)
	spec := wsRunnerSpec(primURL, make(chan []byte, 8))
	spec.Candidates = []WSCandidate{{URL: primURL}, {URL: backURL}}
	spec.ReadIdle = 5 * time.Second // pong 续期保活——读限不参与
	spec.PingEvery = 50 * time.Millisecond
	spec.DataIdle = 250 * time.Millisecond // >PingEvery，僵尸窗必杀
	spec.ReconnectBackoff = 30 * time.Millisecond
	spec.CooldownKills = 2
	spec.CooldownWindow = 700 * time.Millisecond // 测试覆写窗（A2「30min」语义等价）
	spec.OnFrame = func(d []byte) {
		select {
		case dataFrames <- d:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunWS(ctx, spec)

	// ① 主候选两连杀→冷却→兜底接管出数（A3：数据续流）。
	got := 0
	deadline := time.Now().Add(4 * time.Second)
	for got < 3 && time.Now().Before(deadline) {
		select {
		case <-dataFrames:
			got++
		case <-time.After(3 * time.Second):
			t.Fatalf("兜底位接管后数据断流 >看门狗窗（got=%d primUp=%d backUp=%d）", got, primUp.Load(), backUp.Load())
		}
	}
	if backUp.Load() < 1 {
		t.Fatalf("连续僵尸杀后未落兜底候选: backUp=%d primUp=%d", backUp.Load(), primUp.Load())
	}
	if primUp.Load() != 2 {
		t.Fatalf("主候选应恰好两连杀后冷却: primUp=%d", primUp.Load())
	}

	// ② 冷却到期自动回主——primUp 再增（兜底期主端恒优先语义）。
	deadline = time.Now().Add(3 * time.Second)
	for primUp.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(30 * time.Millisecond)
	}
	if primUp.Load() < 3 {
		t.Fatalf("冷却到期未回主: primUp=%d backUp=%d", primUp.Load(), backUp.Load())
	}
}

// TestD541_HealthyResetsKills 健康连接（数据流正常结束）→连续杀计数清零——
// 冷却只累计僵尸杀，不误记正常重连。
func TestD541_HealthyResetsKills(t *testing.T) {
	st := &wsCandState{}
	st.cur.Store(-1)
	st.dataKil.Store(false)
	st.dialOrder(2)
	st.cur.Store(0)
	// 先记一次僵尸杀，再健康收尾→计数应清零。
	st.dataKil.Store(true)
	st.noteExit(WSSpec{CooldownKills: 5, CooldownWindow: time.Minute})
	if st.kills[0] != 1 {
		t.Fatalf("僵尸杀未计数: %d", st.kills[0])
	}
	st.dataKil.Store(false)
	st.noteExit(WSSpec{CooldownKills: 5, CooldownWindow: time.Minute})
	if st.kills[0] != 0 {
		t.Fatalf("健康收尾未清零: %d", st.kills[0])
	}
	if !st.cooled[0].IsZero() {
		t.Fatal("健康收尾不应冷却")
	}
}
