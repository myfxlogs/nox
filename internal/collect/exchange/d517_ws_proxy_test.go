// d517_ws_proxy_test.go —— D-517 S4：fstream 迁移三路径 wsViaProxy 断言。
// 置位/置空两态 + /public /market /private 三前缀映射。
package exchange

import (
	"os"
	"testing"
)

func TestD517_WSViaProxy_FstreamRoutes(t *testing.T) {
	// 置空 = 直连模式（native 端点原样返回）。
	os.Unsetenv("ARB_CEX_WS_BASE")
	cases := []struct {
		name       string
		proxyPath  string
		native     string
		wantProxyd bool
		wantURL    string
	}{
		{"public bookTicker", "/public/ws", "wss://fstream.binance.com/public/ws", false, "wss://fstream.binance.com/public/ws"},
		{"market markPrice", "/market/ws", "wss://fstream.binance.com/market/ws", false, "wss://fstream.binance.com/market/ws"},
		{"private user data", "/private/ws", "wss://fstream.binance.com/private/ws", false, "wss://fstream.binance.com/private/ws"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wsViaProxy(tc.proxyPath, tc.native)
			if got != tc.wantURL {
				t.Errorf("direct: got %s want %s", got, tc.wantURL)
			}
		})
	}

	// 置位 = 经 cex-proxy（base+proxyPath）。
	t.Setenv("ARB_CEX_WS_BASE", "https://192.229.85.35:8443")
	for _, tc := range cases {
		t.Run(tc.name+"/proxied", func(t *testing.T) {
			got := wsViaProxy(tc.proxyPath, tc.native)
			want := "wss://192.229.85.35:8443" + tc.proxyPath
			if got != want {
				t.Errorf("proxied: got %s want %s", got, want)
			}
		})
	}
}

// TestD517_BinanceWSURL_PrivatePath D-517 S3：user_ws real perp 路径断言 /private/ws/{key}。
func TestD517_BinanceWSURL_PrivatePath(t *testing.T) {
	b := &BinanceUserWS{baseURL: "https://192.229.85.35:8443", venueType: "perp"}
	url, err := b.WSURL("testKey123")
	if err != nil {
		t.Fatal(err)
	}
	if url != "wss://192.229.85.35:8443/private/ws/testKey123" {
		t.Errorf("WSURL = %s, want wss://…/private/ws/testKey123", url)
	}
	// spot 不变。
	b2 := &BinanceUserWS{baseURL: "https://192.229.85.35:8443", venueType: "spot"}
	url2, err := b2.WSURL("spotKey")
	if err != nil {
		t.Fatal(err)
	}
	if url2 != "wss://192.229.85.35:8443/spotws/ws/spotKey" {
		t.Errorf("spot WSURL = %s", url2)
	}
	// demo 不迁移。
	b3 := &BinanceUserWS{baseURL: "https://192.229.85.35:8443", venueType: "perp", simulated: true}
	url3, err := b3.WSURL("demoKey")
	if err != nil {
		t.Fatal(err)
	}
	if url3 != "wss://192.229.85.35:8443/demofws/ws/demoKey" {
		t.Errorf("demo WSURL = %s", url3)
	}
}
