package exchange

import (
	"arb/internal/httpx"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ARB_CEX_WS_BASE 置位 → 三所端点改经 cex-proxy 前缀路径。
func TestWSViaProxy_Proxied(t *testing.T) {
	SetProxyEndpoints(ProxyEndpoints{CexWSBase: "wss://192.229.85.35:8443"})
	defer SetProxyEndpoints(ProxyEndpoints{})
	cases := map[string][2]string{
		"wss://192.229.85.35:8443/okxws/ws/v5/public":  {"/okxws/ws/v5/public", okxPublicWSURL},
		"wss://192.229.85.35:8443/okxws/ws/v5/private": {"/okxws/ws/v5/private", okxPrivateWSURL},
		"wss://192.229.85.35:8443/gatespotws/ws/v4/":   {"/gatespotws/ws/v4/", gateSpotWSURL},
		"wss://192.229.85.35:8443/v4/ws/usdt":          {"/v4/ws/usdt", gateFuturesWSURL},
	}
	for want, in := range cases {
		if got := wsViaProxy(in[0], in[1]); got != want {
			t.Fatalf("wsViaProxy(%q)=%q want %q", in[0], got, want)
		}
	}
}

// ARB_CEX_WS_BASE 空 → 原样直连（默认行为不变）。
func TestWSViaProxy_Direct(t *testing.T) {
	SetProxyEndpoints(ProxyEndpoints{})
	defer SetProxyEndpoints(ProxyEndpoints{})
	if got := wsViaProxy("/okxws/ws/v5/public", okxPublicWSURL); got != okxPublicWSURL {
		t.Fatalf("direct mode rewrote url: %q", got)
	}
	if _, h := wsDialer(false); h != nil {
		t.Fatal("direct dialer must not inject proxy token header")
	}
}

// https/http 入参归一 wss/ws（compose 与 REST 端点共用值形）。
func TestCexWSBase_Normalize(t *testing.T) {
	SetProxyEndpoints(ProxyEndpoints{CexWSBase: "https://192.229.85.35:8443/"})
	defer SetProxyEndpoints(ProxyEndpoints{})
	if got := cexWSBase(); got != "wss://192.229.85.35:8443" {
		t.Fatalf("normalize failed: %q", got)
	}
}

// 构造器集成：置位后 wsURL/spotURL/futuresURL 指代理路径，dialer 带 token 头。
func TestWSConstructors_Proxied(t *testing.T) {
	SetProxyEndpoints(ProxyEndpoints{CexWSBase: "wss://192.229.85.35:8443"})
	httpx.Configure("", "tok-x", nil)
	defer func() { SetProxyEndpoints(ProxyEndpoints{}); httpx.Configure("", "", nil) }()
	o := NewOKXWebSocket(Config{})
	if o.wsURLs[0] != "wss://192.229.85.35:8443/okxws/ws/v5/public" {
		t.Fatalf("okx wsURL=%q", o.wsURLs[0])
	}
	if o.header.Get("X-Proxy-Token") != "tok-x" {
		t.Fatal("okx handshake header missing X-Proxy-Token")
	}
	g := NewGateWebSocket(Config{})
	if g.spotURLs[0] != "wss://192.229.85.35:8443/gatespotws/ws/v4/" || g.futuresURLs[0] != "wss://192.229.85.35:8443/v4/ws/usdt" {
		t.Fatalf("gate urls=%q %q", g.spotURLs[0], g.futuresURLs[0])
	}
	op := NewOKXPrivateWS("k", "s", "p", false, nil, "okx:perp", nil, nil, nil, OKXPrivateHooks{})
	if op.wsURLs[0] != "wss://192.229.85.35:8443/okxws/ws/v5/private" {
		t.Fatalf("okx private wsURL=%q", op.wsURLs[0])
	}
}

// 直连构造：URL 原生、header nil。
func TestWSConstructors_Direct(t *testing.T) {
	SetProxyEndpoints(ProxyEndpoints{})
	httpx.Configure("", "", nil)
	defer func() { SetProxyEndpoints(ProxyEndpoints{}); httpx.Configure("", "", nil) }()
	o := NewOKXWebSocket(Config{})
	if o.wsURLs[0] != okxPublicWSURL || o.header != nil {
		t.Fatalf("direct okx mutated: %q hdr=%v", o.wsURLs[0], o.header)
	}
	g := NewGateWebSocket(Config{})
	if g.spotURLs[0] != gateSpotWSURL || g.futuresURLs[0] != gateFuturesWSURL {
		t.Fatalf("direct gate mutated: %q %q", g.spotURLs[0], g.futuresURLs[0])
	}
}

// 双基址有序候选：ARB_CEX_WS_BASE 主 + ARB_CEX_WS_BASE_BACKUP 备（主在前）；
// 直连模式（双空）→ [native]。
func TestWSViaProxyURLs_OrderedCandidates(t *testing.T) {
	SetProxyEndpoints(ProxyEndpoints{CexWSBase: "wss://proxy-a:8443", CexWSBaseBackup: "https://proxy-b:8443/"})
	defer SetProxyEndpoints(ProxyEndpoints{})
	got := wsViaProxyURLs("/okxws/ws/v5/public", okxPublicWSURL)
	want := []string{"wss://proxy-a:8443/okxws/ws/v5/public", "wss://proxy-b:8443/okxws/ws/v5/public"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("candidates=%v want %v", got, want)
	}

	SetProxyEndpoints(ProxyEndpoints{})
	if got := wsViaProxyURLs("/okxws/x", okxPublicWSURL); len(got) != 1 || got[0] != okxPublicWSURL {
		t.Fatalf("direct mode=%v", got)
	}
}

// 构造器双端点：主备两候选进 wsURLs，token 头仍在（两端点同为代理）。
func TestWSConstructors_BackupCandidates(t *testing.T) {
	SetProxyEndpoints(ProxyEndpoints{CexWSBase: "wss://proxy-a:8443", CexWSBaseBackup: "wss://proxy-b:8443"})
	httpx.Configure("", "tok-x", nil)
	defer func() { SetProxyEndpoints(ProxyEndpoints{}); httpx.Configure("", "", nil) }()
	o := NewOKXWebSocket(Config{})
	if len(o.wsURLs) != 2 ||
		o.wsURLs[1] != "wss://proxy-b:8443/okxws/ws/v5/public" {
		t.Fatalf("okx wsURLs=%v", o.wsURLs)
	}
	op := NewOKXPrivateWS("k", "s", "p", false, nil, "okx:perp", nil, nil, nil, OKXPrivateHooks{})
	if len(op.wsURLs) != 2 || op.wsURLs[1] != "wss://proxy-b:8443/okxws/ws/v5/private" {
		t.Fatalf("okx private wsURLs=%v", op.wsURLs)
	}
	g := NewGateWebSocket(Config{})
	if len(g.spotURLs) != 2 || g.spotURLs[1] != "wss://proxy-b:8443/gatespotws/ws/v4/" {
		t.Fatalf("gate spotURLs=%v", g.spotURLs)
	}
}

// binance_market_ws：BINANCE_PROXY_URL_BACKUP 置位→perp/spot 各追加备端候选；
// 空→单候选（行为不变）。D-551 起候选升 WSCandidate 级（URL 字段断言同义）。
func TestBinanceMarketWS_BackupCandidates(t *testing.T) {
	urls := func(cs []WSCandidate) []string {
		us := make([]string, len(cs))
		for i, c := range cs {
			us[i] = c.URL
		}
		return us
	}
	SetProxyEndpoints(ProxyEndpoints{BinanceProxyBackup: "https://proxy-b:8443"})
	defer SetProxyEndpoints(ProxyEndpoints{})
	b := NewBinanceMarketWS(Config{BinanceBaseURL: "https://proxy-a:8443/fapi"})
	pub, mkt, spt := urls(b.publicCands), urls(b.marketCands), urls(b.spotCands)
	if len(pub) != 2 || pub[1] != "wss://proxy-b:8443/public/ws" ||
		len(mkt) != 2 || mkt[1] != "wss://proxy-b:8443/market/ws" ||
		len(spt) != 2 || spt[1] != "wss://proxy-b:8443/spotws/ws" {
		t.Fatalf("public=%v market=%v spot=%v", pub, mkt, spt)
	}
	SetProxyEndpoints(ProxyEndpoints{})
	b = NewBinanceMarketWS(Config{BinanceBaseURL: "https://proxy-a:8443/fapi"})
	if len(b.publicCands) != 1 || len(b.marketCands) != 1 || len(b.spotCands) != 1 {
		t.Fatalf("backup 空应单候选: %v %v %v", b.publicCands, b.marketCands, b.spotCands)
	}
	// D-519 A6（D6）：ws:// 明文备端不被升 wss（内网备端场景）。
	SetProxyEndpoints(ProxyEndpoints{BinanceProxyBackup: "ws://proxy-c:8080"})
	b = NewBinanceMarketWS(Config{BinanceBaseURL: "https://proxy-a:8443/fapi"})
	if b.publicCands[1].URL != "ws://proxy-c:8080/public/ws" {
		t.Fatalf("ws:// 备端被错升 wss: %v", b.publicCands)
	}
}

// binance_user_ws backupWSURL：host 换备端、路径/key 不变；env 空/非法→无候选。
func TestBinanceUserWS_BackupWSURL(t *testing.T) {
	b := &BinanceUserWS{}
	SetProxyEndpoints(ProxyEndpoints{BinanceProxyBackup: "https://proxy-b:8443"})
	defer SetProxyEndpoints(ProxyEndpoints{})
	got := b.backupWSURL("wss://proxy-a:8443/ws/abc123")
	if got != "wss://proxy-b:8443/ws/abc123" {
		t.Fatalf("backupWSURL=%q", got)
	}
	SetProxyEndpoints(ProxyEndpoints{})
	if b.backupWSURL("wss://proxy-a:8443/ws/x") != "" {
		t.Fatal("backup 空应无候选")
	}
}

// D-519 A5（D4）：gate 私有流主端拨失败 → host 换 ARB_CEX_WS_BASE_BACKUP 重试一次；
// env 空 → 单 dial 零额外尝试（计数断言=零行为变化面）。
func TestGatePrivateWS_DialFailover(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"user":"1"}`))
	}))
	defer srv.Close()

	g := NewGatePrivateWS("k", "s", srv.URL+"/api/v4", "gate:perp", false, nil, nil, nil, nil)
	var tried []string
	g.dial = func(_ context.Context, u string) (*websocket.Conn, error) {
		tried = append(tried, u)
		return nil, errors.New("refused")
	}

	SetProxyEndpoints(ProxyEndpoints{CexWSBaseBackup: "wss://proxy-b:8443"})
	defer SetProxyEndpoints(ProxyEndpoints{})
	if err := g.runOnce(context.Background()); err == nil {
		t.Fatal("双端全拒应上抛 dial err")
	}
	main := strings.Replace(srv.URL, "http://", "ws://", 1) + "/v4/ws/usdt"
	if len(tried) != 2 || tried[0] != main || tried[1] != "ws://proxy-b:8443/v4/ws/usdt" {
		t.Fatalf("tried=%v want [%s ws://proxy-b:8443/v4/ws/usdt]", tried, main)
	}

	// 备端空 → 单 dial。
	SetProxyEndpoints(ProxyEndpoints{})
	tried = nil
	_ = g.runOnce(context.Background())
	if len(tried) != 1 {
		t.Fatalf("备端空应单 dial, tried=%v", tried)
	}
}

// D-519 R2 A8（F1 实证）：预取消 ctx 进 runOnce——候选循环 ctx 守卫在拨号后，
// 必须返回 error 而非 nil-conn 落穿 panic（守卫在循环顶时 conn=nil/err=nil 穿透）。
func TestOKXPrivateWS_PreCanceledCtxNoPanic(t *testing.T) {
	o := &OKXPrivateWS{
		wsURLs: []string{"ws://127.0.0.1:1/x"},
		dialer: websocket.DefaultDialer,
		now:    time.Now,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := o.runOnce(ctx); err == nil {
		t.Fatal("预取消 ctx 应返回 error（nil conn 落穿=panic 缺陷形态）")
	}
}
