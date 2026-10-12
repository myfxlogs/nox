// d109_rest_fallback_test.go —— D-109 锚：死流 REST 看门狗兜底——dephealth 红窗才拉
// （绿窗零拉取）、流分键独立闸（markPrice 红不连带 ticker）、facts 与 WS 轨同构
// （funding 年化/next_settle/interval/index_price、vol24h）+有效集过滤+REST 轨专属 src。
package exchange

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nox/internal/data/fact"
)

func d109WS(baseURL string, client *http.Client, degradedFn func(string) bool, sink *[]fact.Fact) *BinanceMarketWS {
	return &BinanceMarketWS{
		cfg: Config{
			Symbols:        []string{"BTCUSDT"},
			BinanceBaseURL: baseURL,
			Client:         client,
		},
		now:        time.Now,
		degradedFn: degradedFn,
		emitFacts: func(_ context.Context, fs []fact.Fact) error {
			*sink = append(*sink, fs...)
			return nil
		},
	}
}

// TestD109_RestFallbackGated 红窗闸：仅 markPrice 键红 → 只拉 premiumIndex、
// ticker24h 零拉取；全绿 → 零拉取（复绿即停）。
func TestD109_RestFallbackGated(t *testing.T) {
	var mktHits, tkrHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/premiumIndex", func(w http.ResponseWriter, _ *http.Request) {
		mktHits.Add(1)
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/fapi/v1/ticker/24hr", func(w http.ResponseWriter, _ *http.Request) {
		tkrHits.Add(1)
		_, _ = w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var got []fact.Fact
	b := d109WS(srv.URL, srv.Client(), func(key string) bool {
		return key == binanceDepKeyPerpMarkPrice
	}, &got)
	b.restFallbackRound(context.Background())
	if mktHits.Load() != 1 || tkrHits.Load() != 0 {
		t.Fatalf("markPrice 红应只拉 premiumIndex: mkt=%d tkr=%d", mktHits.Load(), tkrHits.Load())
	}

	// 全绿：零拉取（复绿即停）。
	green := d109WS(srv.URL, srv.Client(), func(string) bool { return false }, &got)
	green.restFallbackRound(context.Background())
	if mktHits.Load() != 1 || tkrHits.Load() != 0 {
		t.Fatalf("绿窗不应拉取: mkt=%d tkr=%d", mktHits.Load(), tkrHits.Load())
	}

	// ticker 键独立红：只拉 24hr。
	tickerRed := d109WS(srv.URL, srv.Client(), func(key string) bool {
		return key == binanceDepKeyPerpTicker
	}, &got)
	tickerRed.restFallbackRound(context.Background())
	if mktHits.Load() != 1 || tkrHits.Load() != 1 {
		t.Fatalf("ticker 红应只拉 24hr: mkt=%d tkr=%d", mktHits.Load(), tkrHits.Load())
	}
}

// TestD109_PremiumIndexFacts premiumIndex→facts 同构：funding 年化（interval 缓存缺省
// 3/天）、next_settle、interval、index_price；有效集外符号不造；src=REST 轨专属。
func TestD109_PremiumIndexFacts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/premiumIndex", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"symbol":"BTCUSDT","indexPrice":"4000.20","lastFundingRate":"0.0001","nextFundingTime":1700000000000,"markPrice":"4001.00"},
			{"symbol":"ETHUSDT","indexPrice":"200.00","lastFundingRate":"0.0001","nextFundingTime":1700000000000}
		]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var got []fact.Fact
	b := d109WS(srv.URL, srv.Client(), func(string) bool { return true }, &got)
	b.fetchPremiumIndex(context.Background())

	if len(got) != 4 {
		t.Fatalf("应 4 行（ETHUSDT 被滤）: %+v", got)
	}
	byKind := map[string]fact.Fact{}
	for _, f := range got {
		if f.Symbol != "BTCUSDT" {
			t.Fatalf("有效集外符号泄漏: %+v", f)
		}
		if f.Src != restFallbackSrcMarkPrice {
			t.Fatalf("src 应 REST 轨: %+v", f)
		}
		byKind[f.Kind] = f
	}
	if v := byKind[fact.KindFunding].Value; v < 10.9499 || v > 10.9501 {
		t.Fatalf("funding 年化应 10.95（0.0001×3/天×365×100）: %v", v)
	}
	if v := byKind[fact.KindFundingNextSettle].Value; v != 1700000000 {
		t.Fatalf("next_settle 应 ms/1000: %v", v)
	}
	if v := byKind[fact.KindFundingInterval].Value; v != 28800 {
		t.Fatalf("interval 应 86400/3: %v", v)
	}
	if v := byKind[fact.KindIndexPrice].Value; v != 4000.20 {
		t.Fatalf("index_price 应 4000.20: %v", v)
	}
}

// TestD109_Ticker24hFacts 24hr→vol24h facts：有效集内才造、src=REST 轨专属。
func TestD109_Ticker24hFacts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/ticker/24hr", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"symbol":"BTCUSDT","quoteVolume":"123456.78"},
			{"symbol":"ETHUSDT","quoteVolume":"999.99"}
		]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var got []fact.Fact
	b := d109WS(srv.URL, srv.Client(), func(string) bool { return true }, &got)
	b.fetchTicker24h(context.Background())

	if len(got) != 1 {
		t.Fatalf("应 1 行（ETHUSDT 被滤）: %+v", got)
	}
	if got[0].Kind != fact.KindVol24h || got[0].Symbol != "BTCUSDT" || got[0].Value != 123456.78 {
		t.Fatalf("vol24h fact 不符: %+v", got[0])
	}
	if got[0].Src != restFallbackSrcTicker {
		t.Fatalf("src 应 REST 轨: %+v", got[0])
	}
}

// rtFunc 测试用 RoundTripper 适配器。
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestD109_FrameAgeGate R1①：帧年龄闸——dephealth 绿但 markPrice/ticker 末帧超龄
// → 仍点火（「dial 成功→ack→静默」形态覆盖，dephealth 连败计数打不中此形态）；
// 帧新到=流活 → 零拉取（复绿即停）。两轨闸独立。
func TestD109_FrameAgeGate(t *testing.T) {
	var mktHits, tkrHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/premiumIndex", func(w http.ResponseWriter, _ *http.Request) {
		mktHits.Add(1)
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/fapi/v1/ticker/24hr", func(w http.ResponseWriter, _ *http.Request) {
		tkrHits.Add(1)
		_, _ = w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var got []fact.Fact
	b := d109WS(srv.URL, srv.Client(), func(string) bool { return false }, &got) // dephealth 全绿

	// 从未交付帧但已超启动基线 5min（生产基线=Run 启动刻，此处直接钉超龄刻）。
	b.lastFrameMarkPrice.Store(time.Now().Add(-restFallbackStaleAfter - time.Minute).UnixNano())
	b.lastFrameTicker.Store(time.Now().UnixNano()) // ticker 帧新鲜
	b.restFallbackRound(context.Background())
	if mktHits.Load() != 1 || tkrHits.Load() != 0 {
		t.Fatalf("markPrice 超龄应点火、ticker 新鲜应停: mkt=%d tkr=%d", mktHits.Load(), tkrHits.Load())
	}

	// markPrice 帧复到 → 闸自落；ticker 转超龄 → 换轨点火。
	b.lastFrameMarkPrice.Store(time.Now().UnixNano())
	b.lastFrameTicker.Store(time.Now().Add(-restFallbackStaleAfter - time.Minute).UnixNano())
	b.restFallbackRound(context.Background())
	if mktHits.Load() != 1 || tkrHits.Load() != 1 {
		t.Fatalf("流复绿应停、转死轨应点: mkt=%d tkr=%d", mktHits.Load(), tkrHits.Load())
	}

	// 两轨帧新鲜 → 全停。
	b.lastFrameTicker.Store(time.Now().UnixNano())
	b.restFallbackRound(context.Background())
	if mktHits.Load() != 1 || tkrHits.Load() != 1 {
		t.Fatalf("全活应零拉取: mkt=%d tkr=%d", mktHits.Load(), tkrHits.Load())
	}
}

// TestD109_NativeNoProxyToken R1②：fallback 拉币安原生域走 restNative 裸 client——
// cfg.Client 模拟 tokenRT（每请求注 X-Proxy-Token）全程零触达；原生域请求面
// 不出现该头。非原生（cex-proxy 路由）仍用 cfg.Client。
func TestD109_NativeNoProxyToken(t *testing.T) {
	var tokenSeen atomic.Bool
	proxyClient := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-Proxy-Token") != "" {
			tokenSeen.Store(true)
		}
		return &http.Response{
			StatusCode: 200, Header: make(http.Header),
			Body:    io.NopCloser(strings.NewReader(`[]`)),
			Request: r,
		}, nil
	})}

	var got []fact.Fact
	b := d109WS("https://fapi.binance.com", proxyClient, func(string) bool { return true }, &got)
	var nativeHits atomic.Int32
	b.restNative = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		nativeHits.Add(1)
		if r.Header.Get("X-Proxy-Token") != "" {
			tokenSeen.Store(true)
		}
		return &http.Response{
			StatusCode: 200, Header: make(http.Header),
			Body:    io.NopCloser(strings.NewReader(`[]`)),
			Request: r,
		}, nil
	})}

	b.fetchPremiumIndex(context.Background())
	b.fetchTicker24h(context.Background())
	if nativeHits.Load() != 2 {
		t.Fatalf("原生域应走 restNative 裸 client 两次: %d", nativeHits.Load())
	}
	if tokenSeen.Load() {
		t.Fatal("X-Proxy-Token 泄漏至币安原生域请求面")
	}
	if got := b.restClientFor("https://cex-proxy.example.com/fapi/v1/premiumIndex"); got != proxyClient {
		t.Fatal("非原生 host 应仍用 cfg.Client")
	}
}
