// d551_ws_endpoint_test.go —— D-551 断言锚：S1 逐流 env 覆盖顺序（T1）、
// token 仅代理 host（T2）、S2 exchangeInfo 过滤订阅（T3）。
package exchange

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

// T1 env 置位→env URL=候选[0]、派生代理 URL=候选[1]；未置位→现行为不变。
func TestD551_T1_EnvOverrideOrder(t *testing.T) {
	for _, e := range []string{"ARB_BINANCE_SPOT_WS_URL", "ARB_BINANCE_PERP_PUBLIC_WS_URL",
		"ARB_BINANCE_PERP_MARKET_WS_URL", "BINANCE_PROXY_URL_BACKUP"} {
		t.Setenv(e, "") // 隔离环境漂移
	}
	for _, tc := range []struct {
		name    string
		env     string
		val     string
		cands   func(*BinanceMarketWS) []WSCandidate
		wantLen int
	}{
		{"spot 置位", "ARB_BINANCE_SPOT_WS_URL", "wss://data-stream.binance.vision/ws",
			func(b *BinanceMarketWS) []WSCandidate { return b.spotCands }, 2},
		{"perp-public 置位", "ARB_BINANCE_PERP_PUBLIC_WS_URL", "wss://fstream.binance.com/ws",
			func(b *BinanceMarketWS) []WSCandidate { return b.publicCands }, 2},
		{"perp-market 置位", "ARB_BINANCE_PERP_MARKET_WS_URL", "wss://fstream.binance.com/ws",
			func(b *BinanceMarketWS) []WSCandidate { return b.marketCands }, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, tc.val)
			b := NewBinanceMarketWS(Config{BinanceBaseURL: "https://cex.myfxlogs.org"})
			cs := tc.cands(b)
			if len(cs) != tc.wantLen {
				t.Fatalf("候选数=%d 期望 %d", len(cs), tc.wantLen)
			}
			if cs[0].URL != tc.val {
				t.Fatalf("候选[0]=%q 期望 env URL %q", cs[0].URL, tc.val)
			}
			if u, _ := url.Parse(cs[1].URL); u.Host != "cex.myfxlogs.org" {
				t.Fatalf("候选[1]=%q 期望代理 host", cs[1].URL)
			}
		})
	}
	t.Run("未置位现行为不变", func(t *testing.T) {
		b := NewBinanceMarketWS(Config{BinanceBaseURL: "https://cex.myfxlogs.org"})
		if len(b.spotCands) != 1 || !strings.HasSuffix(b.spotCands[0].URL, "/spotws/ws") {
			t.Fatalf("spotCands=%v", b.spotCands)
		}
	})
}

// T2 X-Proxy-Token 仅对代理 host 发出；币安原生 host=空头。
func TestD551_T2_TokenOnlyProxyHost(t *testing.T) {
	t.Setenv("ARB_BINANCE_PROXY_TOKEN", "tok-sec")
	t.Setenv("ARB_BINANCE_SPOT_WS_URL", "wss://data-stream.binance.vision/ws")
	t.Setenv("BINANCE_PROXY_URL_BACKUP", "wss://backup.proxy.example")
	b := NewBinanceMarketWS(Config{BinanceBaseURL: "https://cex.myfxlogs.org"})
	if got := b.spotCands[0].Header.Get("X-Proxy-Token"); got != "" {
		t.Fatalf("原生域候选带 token=%q（泄露）", got)
	}
	for i, want := range []string{"tok-sec", "tok-sec"} {
		if got := b.spotCands[i+1].Header.Get("X-Proxy-Token"); got != want {
			t.Fatalf("代理候选[%d] token=%q 期望 %q", i+1, got, want)
		}
	}
	// env 指回代理 host 时仍带 token。
	t.Setenv("ARB_BINANCE_SPOT_WS_URL", "wss://cex.myfxlogs.org/spotws/ws")
	b2 := NewBinanceMarketWS(Config{BinanceBaseURL: "https://cex.myfxlogs.org"})
	if got := b2.spotCands[0].Header.Get("X-Proxy-Token"); got != "tok-sec" {
		t.Fatalf("env 指代理 host token=%q", got)
	}
}

// T3 exchangeInfo stub 2 符号→spot 订阅只出 2 参数；stub 失败→fail-open 全量。
func TestD551_T3_SpotInfoFilter(t *testing.T) {
	t.Setenv("ARB_BINANCE_SPOT_INFO_URL", "")
	t.Run("有效集过滤", func(t *testing.T) {
		b := NewBinanceMarketWS(Config{
			BinanceBaseURL: "https://cex.myfxlogs.org",
			Symbols:        []string{"BTC", "SKL", "XAU"},
		})
		calls := 0
		b.spotInfoFetch = func(_ context.Context, _ string) (map[string]struct{}, error) {
			calls++
			return map[string]struct{}{"BTCUSDT": {}, "SKLUSDT": {}}, nil
		}
		b.ensureSpotInfo(context.Background())
		var params []string
		for _, sym := range b.cfg.effectiveSymbols("binance") {
			if b.spotOK(sym) {
				params = append(params, strings.ToLower(sym+"usdt")+"@bookTicker")
			}
		}
		if len(params) != 2 || params[0] != "btcusdt@bookTicker" || params[1] != "sklusdt@bookTicker" {
			t.Fatalf("params=%v", params)
		}
		b.ensureSpotInfo(context.Background()) // once：二次调用不再拉
		if calls != 1 {
			t.Fatalf("fetch 次数=%d", calls)
		}
	})
	t.Run("拉取失败 fail-open", func(t *testing.T) {
		b := NewBinanceMarketWS(Config{
			BinanceBaseURL: "https://cex.myfxlogs.org",
			Symbols:        []string{"BTC", "XAU"},
		})
		b.spotInfoFetch = func(_ context.Context, _ string) (map[string]struct{}, error) {
			return nil, errors.New("dial fail")
		}
		b.ensureSpotInfo(context.Background())
		if !b.spotOK("XAU") || !b.spotOK("BTC") {
			t.Fatal("fail-open 应全量放行")
		}
	})
	t.Run("原生域 infoURL 裸客户端", func(t *testing.T) {
		b := NewBinanceMarketWS(Config{
			BinanceBaseURL:     "https://cex.myfxlogs.org",
			BinanceSpotInfoURL: "https://data-api.binance.vision/api/v3/exchangeInfo",
		})
		if b.spotInfoClient != nil && b.spotInfoClient == b.cfg.Client {
			t.Fatal("原生域不应复用 cfg.Client（带 token）")
		}
	})
}
