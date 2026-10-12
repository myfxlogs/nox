// d106_binance_native_test.go —— D-106 锚：binance WS 原生端点分支+token 收敛——
// base 指币安原生域（*.binance.com）→ 用户流/ws-api 走官方端点形制；cex-proxy 基址
// → 既有代理路由形制原样保留（回归）；dial 握手头与 REST client 按域收敛（原生域
// 零 X-Proxy-Token 出域）。
package exchange

import (
	"net/http"
	"testing"
)

// TestD106_UserWSURLNative 原生分支：perp base（fapi 域）→ fstream /ws/<key>；
// spot base（api 域）→ stream:9443 /ws/<key>——REST host 域名与 WS host 域名不同，恒映射。
func TestD106_UserWSURLNative(t *testing.T) {
	perp := &BinanceUserWS{baseURL: "https://fapi.binance.com/fapi", venueType: "perp"}
	got, err := perp.WSURL("KEY123")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wss://fstream.binance.com/ws/KEY123" {
		t.Fatalf("perp native WSURL=%s", got)
	}
	spot := &BinanceUserWS{baseURL: "https://api.binance.com/api", venueType: "spot"}
	got, err = spot.WSURL("KEY123")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wss://stream.binance.com:9443/ws/KEY123" {
		t.Fatalf("spot native WSURL=%s", got)
	}
}

// TestD106_UserWSURLProxy 代理分支回归：cex-proxy 基址 → /private/ws、/spotws/ws 形制不变。
func TestD106_UserWSURLProxy(t *testing.T) {
	perp := &BinanceUserWS{baseURL: "https://192.229.85.35:8443/fapi", venueType: "perp"}
	got, err := perp.WSURL("KEY123")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wss://192.229.85.35:8443/private/ws/KEY123" {
		t.Fatalf("perp proxy WSURL=%s", got)
	}
	spot := &BinanceUserWS{baseURL: "https://cex.myfxlogs.org/api", venueType: "spot"}
	got, err = spot.WSURL("KEY123")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wss://cex.myfxlogs.org/spotws/ws/KEY123" {
		t.Fatalf("spot proxy WSURL=%s", got)
	}
}

// TestD106_WsapiURLNative ws-api 原生分支：base 指原生域 → 官方端点；代理形制回归由
// TestBinanceSpotWsapiURL 钉守。
func TestD106_WsapiURLNative(t *testing.T) {
	b := &BinanceUserWS{baseURL: "https://api.binance.com/api"}
	u, err := b.wsapiURL()
	if err != nil {
		t.Fatal(err)
	}
	if u != "wss://ws-api.binance.com/ws-api/v3" {
		t.Fatalf("native wsapiURL=%s", u)
	}
}

// TestD106_BinanceWSDialHeader token 收敛：原生域空头（nil），代理域回传代理头原样。
func TestD106_BinanceWSDialHeader(t *testing.T) {
	proxy := http.Header{"X-Proxy-Token": []string{"t"}}
	if h := binanceWSDialHeader("wss://fstream.binance.com/ws/k", proxy); h != nil {
		t.Fatalf("原生域应空头: %v", h)
	}
	if h := binanceWSDialHeader("wss://192.229.85.35:8443/private/ws/k", proxy); h == nil || h.Get("X-Proxy-Token") != "t" {
		t.Fatalf("代理域应带 token 头: %v", h)
	}
}

// TestD106_RestForByHost REST client 按域收敛：原生域=裸客户端（与代理 client 分离实例）。
func TestD106_RestForByHost(t *testing.T) {
	b := NewBinanceUserWS("k", "s", "https://fapi.binance.com/fapi", "perp", "binance:perp", false,
		nil, nil, nil, nil, BinanceUserHooks{})
	if b.restFor("https://fapi.binance.com/fapi/v1/listenKey") == b.rest {
		t.Fatal("原生域应走裸客户端（防 tokenRT 注入）")
	}
	if b.restFor("https://192.229.85.35:8443/fapi/v1/listenKey") != b.rest {
		t.Fatal("代理域应走代理 client（token+CA）")
	}
}
