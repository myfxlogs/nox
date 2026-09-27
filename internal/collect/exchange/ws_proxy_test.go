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
	if o.wsURL != "wss://192.229.85.35:8443/okxws/ws/v5/public" {
		t.Fatalf("okx wsURL=%q", o.wsURL)
	}
	if o.header.Get("X-Proxy-Token") != "tok-x" {
		t.Fatal("okx handshake header missing X-Proxy-Token")
	}
	g := NewGateWebSocket(Config{})
	if g.spotURL != "wss://192.229.85.35:8443/gatespotws/ws/v4/" || g.futuresURL != "wss://192.229.85.35:8443/v4/ws/usdt" {
		t.Fatalf("gate urls=%q %q", g.spotURL, g.futuresURL)
	}
	op := NewOKXPrivateWS("k", "s", "p", false, nil, "okx:perp", nil, nil, nil, OKXPrivateHooks{})
	if op.wsURL != "wss://192.229.85.35:8443/okxws/ws/v5/private" {
		t.Fatalf("okx private wsURL=%q", op.wsURL)
	}
}

// 直连构造：URL 原生、header nil。
func TestWSConstructors_Direct(t *testing.T) {
	t.Setenv("ARB_CEX_WS_BASE", "")
	o := NewOKXWebSocket(Config{})
	if o.wsURL != okxPublicWSURL || o.header != nil {
		t.Fatalf("direct okx mutated: %q hdr=%v", o.wsURL, o.header)
	}
	g := NewGateWebSocket(Config{})
	if g.spotURL != gateSpotWSURL || g.futuresURL != gateFuturesWSURL {
		t.Fatalf("direct gate mutated: %q %q", g.spotURL, g.futuresURL)
	}
}
