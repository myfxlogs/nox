// okx_fetch_test.go —— D-011 S2：OKX 直连→cex-proxy 回落骨架测试。
// 覆盖回落触发边界：仅传输层错误回落（failoverRT 同构）；HTTP 到达（4xx/5xx/业务码）不回落；
// okxBase 空=仅直连；回落失败原直连错上抛。
package cexscan

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// deadServer 起一个随即关闭的服务器——其 URL 连接必被拒（传输层错误形态）。
func deadServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	return srv.URL
}

// countServer 计请求数并回 JSON body（校验回落是否发生、打到哪个基址）。
func countServer(t *testing.T, status int, body string) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// withDirectBase 测试期覆写直连基址（okxDirectBase 为包级 var）。
func withDirectBase(t *testing.T, base string) {
	t.Helper()
	old := okxDirectBase
	okxDirectBase = base
	t.Cleanup(func() { okxDirectBase = old })
}

func okxTickerBody(last string) string {
	b, _ := json.Marshal(map[string]any{"code": "0", "data": []map[string]string{{"last": last}}})
	return string(b)
}

// TestOKxFetch_DirectSuccNoFallback 直连成功 → 请求计数=0（回落基址零触碰）。
func TestOKxFetch_DirectSuccNoFallback(t *testing.T) {
	directSrv, directHits := countServer(t, 200, okxTickerBody("100"))
	proxySrv, proxyHits := countServer(t, 200, "{}")
	withDirectBase(t, directSrv.URL)

	body, err := okxFetch(context.Background(), directSrv.Client(), proxySrv.URL, "/api/v5/t", nil)
	if err != nil {
		t.Fatalf("okxFetch: %v", err)
	}
	if !strings.Contains(string(body), `"last":"100"`) {
		t.Fatalf("body = %s, want direct payload", body)
	}
	if *directHits != 1 || *proxyHits != 0 {
		t.Fatalf("hits direct=%d proxy=%d, want 1/0", *directHits, *proxyHits)
	}
}

// TestOKxFetch_TransportErrFallsBack 直连传输层错误 → 回落成功，src=proxy 数据生效。
func TestOKxFetch_TransportErrFallsBack(t *testing.T) {
	withDirectBase(t, deadServer(t))
	proxySrv, proxyHits := countServer(t, 200, okxTickerBody("200"))

	body, err := okxFetch(context.Background(), proxySrv.Client(), proxySrv.URL, "/api/v5/t", nil)
	if err != nil {
		t.Fatalf("okxFetch: %v", err)
	}
	if !strings.Contains(string(body), `"last":"200"`) {
		t.Fatalf("body = %s, want proxy payload", body)
	}
	if *proxyHits != 1 {
		t.Fatalf("proxy hits = %d, want 1", *proxyHits)
	}
}

// TestOKxFetch_HTTPArrivedNoFallback 直连 5xx（HTTP 已到达）→ 不回落（venue 语义非链路故障）。
func TestOKxFetch_HTTPArrivedNoFallback(t *testing.T) {
	directSrv, directHits := countServer(t, http.StatusBadGateway, `{"code":"50001"}`)
	proxySrv, proxyHits := countServer(t, 200, okxTickerBody("300"))
	withDirectBase(t, directSrv.URL)

	if _, err := okxFetch(context.Background(), directSrv.Client(), proxySrv.URL, "/api/v5/t", nil); err == nil {
		t.Fatal("want error on 502 direct")
	}
	if *proxyHits != 0 {
		t.Fatalf("proxy hits = %d, want 0（HTTP 到达不回落）", *proxyHits)
	}
	if *directHits != 1 {
		t.Fatalf("direct hits = %d, want 1", *directHits)
	}
}

// TestOKxFetch_BusinessCodeNoFallback 直连 200 但 code≠0 → 不回落（品种映射语义如 51001）。
// 业务码错误在解析层浮出（okxFetch 层面 body/err 双 nil 错语义不适用），故经 fetchOKXTicker 断言。
func TestOKxFetch_BusinessCodeNoFallback(t *testing.T) {
	directSrv, _ := countServer(t, 200, `{"code":"51001","msg":"Instrument ID does not exist"}`)
	proxySrv, proxyHits := countServer(t, 200, okxTickerBody("400"))
	withDirectBase(t, directSrv.URL)

	_, err := fetchOKXTicker(context.Background(), directSrv.Client(), proxySrv.URL, "BTC")
	if err == nil || !strings.Contains(err.Error(), "51001") {
		t.Fatalf("err = %v, want 51001 业务码错误", err)
	}
	if *proxyHits != 0 {
		t.Fatalf("proxy hits = %d, want 0（业务码不回落）", *proxyHits)
	}
}

// TestOKxFetch_NoBaseDirectOnly okxBase 空（无 okx spot 配置行）→ 保持仅直连不造回落。
func TestOKxFetch_NoBaseDirectOnly(t *testing.T) {
	withDirectBase(t, deadServer(t))
	proxySrv, proxyHits := countServer(t, 200, okxTickerBody("500"))

	if _, err := okxFetch(context.Background(), proxySrv.Client(), "", "/api/v5/t", nil); err == nil {
		t.Fatal("want transport error without fallback base")
	}
	if *proxyHits != 0 {
		t.Fatalf("proxy hits = %d, want 0（空基址不回落）", *proxyHits)
	}
}

// TestOKxFetch_BothFail 直连与回落皆败（两死服务=传输层双故障）→ 原直连错上抛且附带代理错。
func TestOKxFetch_BothFail(t *testing.T) {
	withDirectBase(t, deadServer(t))
	proxyDead := deadServer(t)

	_, err := okxFetch(context.Background(), http.DefaultClient, proxyDead, "/api/v5/t", nil)
	if err == nil {
		t.Fatal("want combined error")
	}
	if !strings.Contains(err.Error(), "direct:") || !strings.Contains(err.Error(), "proxy:") {
		t.Fatalf("err = %v, want direct+proxy 因果链", err)
	}
}

// TestFetchOKXTicker_FallbackE2E spread 采集面端到端：直连死 → 代理活 → 价格取到。
func TestFetchOKXTicker_FallbackE2E(t *testing.T) {
	withDirectBase(t, deadServer(t))
	proxySrv, _ := countServer(t, 200, okxTickerBody("123.45"))

	price, err := fetchOKXTicker(context.Background(), proxySrv.Client(), proxySrv.URL, "BTC")
	if err != nil {
		t.Fatalf("fetchOKXTicker: %v", err)
	}
	if price != 123.45 {
		t.Fatalf("price = %v, want 123.45", price)
	}
}

// TestCollectOKX_FallbackE2E earn 采集面端到端：直连死 → cex-proxy 活 → APR 取到（签名头随试新鲜）。
func TestCollectOKX_FallbackE2E(t *testing.T) {
	withDirectBase(t, deadServer(t))
	var paths []string
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"code":"0","data":[{"ccy":"USDT","estRate":"0.035"}]}`))
	}))
	defer srv.Close()

	c := &EarnCollector{client: srv.Client(), okxKey: "k", okxSecret: "s", okxPass: "p", okxBase: srv.URL}
	aprs, err := c.CollectOKX(context.Background())
	if err != nil {
		t.Fatalf("CollectOKX: %v", err)
	}
	if len(aprs) != 1 || aprs[0].APR != 0.035 || aprs[0].Ccy != "USDT" {
		t.Fatalf("aprs = %+v, want USDT/0.035", aprs)
	}
	if hits != 1 || len(paths) != 1 || paths[0] != okxEarnPath {
		t.Fatalf("proxy request path = %v hits=%d, want [%s]×1", paths, hits, okxEarnPath)
	}
}
