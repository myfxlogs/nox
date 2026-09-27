package exchange

import "testing"

// ARB_CEX_WS_BASE 置位 → 三所端点改经 cex-proxy 前缀路径。
func TestWSViaProxy_Proxied(t *testing.T) {
	t.Setenv("ARB_CEX_WS_BASE", "wss://192.229.85.35:8443")
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
	t.Setenv("ARB_CEX_WS_BASE", "")
	if got := wsViaProxy("/okxws/ws/v5/public", okxPublicWSURL); got != okxPublicWSURL {
		t.Fatalf("direct mode rewrote url: %q", got)
	}
	if _, h := wsDialer(false); h != nil {
		t.Fatal("direct dialer must not inject proxy token header")
	}
}

// https/http 入参归一 wss/ws（compose 与 REST 端点共用值形）。
func TestCexWSBase_Normalize(t *testing.T) {
	t.Setenv("ARB_CEX_WS_BASE", "https://192.229.85.35:8443/")
	if got := cexWSBase(); got != "wss://192.229.85.35:8443" {
		t.Fatalf("normalize failed: %q", got)
	}
}

// 构造器集成：置位后 wsURL/spotURL/futuresURL 指代理路径，dialer 带 token 头。
func TestWSConstructors_Proxied(t *testing.T) {
	t.Setenv("ARB_CEX_WS_BASE", "wss://192.229.85.35:8443")
	t.Setenv("ARB_BINANCE_PROXY_TOKEN", "tok-x")
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
	t.Setenv("ARB_CEX_WS_BASE", "")
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
	t.Setenv("ARB_CEX_WS_BASE", "wss://proxy-a:8443")
	t.Setenv("ARB_CEX_WS_BASE_BACKUP", "https://proxy-b:8443/") // 归一 wss + 去尾斜
	got := wsViaProxyURLs("/okxws/ws/v5/public", okxPublicWSURL)
	want := []string{"wss://proxy-a:8443/okxws/ws/v5/public", "wss://proxy-b:8443/okxws/ws/v5/public"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("candidates=%v want %v", got, want)
	}

	t.Setenv("ARB_CEX_WS_BASE", "")
	t.Setenv("ARB_CEX_WS_BASE_BACKUP", "")
	if got := wsViaProxyURLs("/okxws/x", okxPublicWSURL); len(got) != 1 || got[0] != okxPublicWSURL {
		t.Fatalf("direct mode=%v", got)
	}
}

// 构造器双端点：主备两候选进 wsURLs，token 头仍在（两端点同为代理）。
func TestWSConstructors_BackupCandidates(t *testing.T) {
	t.Setenv("ARB_CEX_WS_BASE", "wss://proxy-a:8443")
	t.Setenv("ARB_CEX_WS_BASE_BACKUP", "wss://proxy-b:8443")
	t.Setenv("ARB_BINANCE_PROXY_TOKEN", "tok-x")
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
// 空→单候选（行为不变）。
func TestBinanceMarketWS_BackupCandidates(t *testing.T) {
	t.Setenv("BINANCE_PROXY_URL_BACKUP", "https://proxy-b:8443")
	b := NewBinanceMarketWS(Config{BinanceBaseURL: "https://proxy-a:8443/fapi"})
	if len(b.publicURLs) != 2 || b.publicURLs[1] != "wss://proxy-b:8443/public/ws" ||
		len(b.marketURLs) != 2 || b.marketURLs[1] != "wss://proxy-b:8443/market/ws" ||
		len(b.spotURLs) != 2 || b.spotURLs[1] != "wss://proxy-b:8443/spotws/ws" {
		t.Fatalf("public=%v market=%v spot=%v", b.publicURLs, b.marketURLs, b.spotURLs)
	}
	t.Setenv("BINANCE_PROXY_URL_BACKUP", "")
	b = NewBinanceMarketWS(Config{BinanceBaseURL: "https://proxy-a:8443/fapi"})
	if len(b.publicURLs) != 1 || len(b.marketURLs) != 1 || len(b.spotURLs) != 1 {
		t.Fatalf("backup 空应单候选: %v %v %v", b.publicURLs, b.marketURLs, b.spotURLs)
	}
}

// binance_user_ws backupWSURL：host 换备端、路径/key 不变；env 空/非法→无候选。
func TestBinanceUserWS_BackupWSURL(t *testing.T) {
	b := &BinanceUserWS{}
	t.Setenv("BINANCE_PROXY_URL_BACKUP", "https://proxy-b:8443")
	got := b.backupWSURL("wss://proxy-a:8443/ws/abc123")
	if got != "wss://proxy-b:8443/ws/abc123" {
		t.Fatalf("backupWSURL=%q", got)
	}
	t.Setenv("BINANCE_PROXY_URL_BACKUP", "")
	if b.backupWSURL("wss://proxy-a:8443/ws/x") != "" {
		t.Fatal("backup 空应无候选")
	}
}
