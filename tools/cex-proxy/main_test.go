package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 用 httptest 上游分别扮演各 CEX，验证令牌闸、最长前缀路由、strip_prefix 与 Host 改写。
func TestHandlerRoutingAndTokenGate(t *testing.T) {
	okxSeen := make(chan string, 1)
	gateSeen := make(chan string, 1)
	apiSeen := make(chan string, 1)
	fapiSeen := make(chan string, 1)
	demoSeen := make(chan string, 1)
	demofapiSeen := make(chan string, 1)

	okxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okxSeen <- r.Host + r.URL.Path
		io.WriteString(w, "okx-ok")
	}))
	defer okxSrv.Close()
	gateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateSeen <- r.Host + r.URL.Path
		io.WriteString(w, "gate-ok")
	}))
	defer gateSrv.Close()
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiSeen <- r.Host + r.URL.Path
		io.WriteString(w, "api-ok")
	}))
	defer apiSrv.Close()
	fapiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fapiSeen <- r.Host + r.URL.Path
		io.WriteString(w, "fapi-ok")
	}))
	defer fapiSrv.Close()
	demoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		demoSeen <- r.Host + r.URL.Path
		io.WriteString(w, "demo-ok")
	}))
	defer demoSrv.Close()
	demofapiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		demofapiSeen <- r.Host + r.URL.Path
		io.WriteString(w, "demofapi-ok")
	}))
	defer demofapiSrv.Close()

	// 故意乱序排列——最长前缀匹配必须自行排序，不依赖配置顺序。
	cfg := Config{Upstreams: []Upstream{
		{Prefix: "/api", Target: apiSrv.URL},
		{Prefix: "/demofapi", Target: demofapiSrv.URL, StripPrefix: true},
		{Prefix: "/api/v5", Target: okxSrv.URL},
		{Prefix: "/fapi", Target: fapiSrv.URL},
		{Prefix: "/demo", Target: demoSrv.URL, StripPrefix: true},
		{Prefix: "/api/v4", Target: gateSrv.URL},
	}}

	h, err := newHandler("secret-token", cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	// 1. 无令牌 → 403
	resp, err := http.Get(ts.URL + "/api/v3/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("无令牌应 403，got %d", resp.StatusCode)
	}

	// 2. /api/v5 → OKX（最长前缀优先于 /api）
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v5/account/balance", nil)
	req.Header.Set("X-Proxy-Token", "secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "okx-ok" {
		t.Fatalf("/api/v5 应命中 OKX 上游，got %q", body)
	}
	if got := <-okxSeen; got != okxSrv.Listener.Addr().String()+"/api/v5/account/balance" {
		t.Fatalf("OKX Host/路径错误，got %q", got)
	}

	// 3. /api/v4 → Gate（最长前缀优先于 /api）
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v4/futures/usdt/accounts", nil)
	req.Header.Set("X-Proxy-Token", "secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "gate-ok" {
		t.Fatalf("/api/v4 应命中 Gate 上游，got %q", body)
	}
	if got := <-gateSeen; got != gateSrv.Listener.Addr().String()+"/api/v4/futures/usdt/accounts" {
		t.Fatalf("Gate Host/路径错误，got %q", got)
	}

	// 4. /api/v3 → Binance spot（/api 兜底，非 /api/v5 也非 /api/v4）
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v3/ping?x=1", nil)
	req.Header.Set("X-Proxy-Token", "secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "api-ok" {
		t.Fatalf("/api/v3 应命中 Binance spot 上游，got %q", body)
	}
	if got := <-apiSeen; got != apiSrv.Listener.Addr().String()+"/api/v3/ping" {
		t.Fatalf("Binance spot Host/路径错误，got %q", got)
	}

	// 5. /fapi → Binance futures
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/fapi/v1/balance", nil)
	req.Header.Set("X-Proxy-Token", "secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "fapi-ok" {
		t.Fatalf("/fapi 应命中 Binance futures 上游，got %q", body)
	}
	if got := <-fapiSeen; got != fapiSrv.Listener.Addr().String()+"/fapi/v1/balance" {
		t.Fatalf("fapi Host/路径错误，got %q", got)
	}

	// 6. /demo → demo 上游，strip_prefix 剥离 /demo
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/demo/api/v3/account", nil)
	req.Header.Set("X-Proxy-Token", "secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "demo-ok" {
		t.Fatalf("/demo 应命中 demo 上游，got %q", body)
	}
	if got := <-demoSeen; got != demoSrv.Listener.Addr().String()+"/api/v3/account" {
		t.Fatalf("demo 应剥离 /demo 前缀且改写 Host，got %q", got)
	}

	// 7. /demofapi → demofapi 上游，strip_prefix 剥离 /demofapi
	//    须先于 /demo 匹配——最长前缀排序保证 /demofapi(9) > /demo(5)。
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/demofapi/fapi/v1/balance", nil)
	req.Header.Set("X-Proxy-Token", "secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "demofapi-ok" {
		t.Fatalf("/demofapi 应命中 demofapi 上游，got %q", body)
	}
	if got := <-demofapiSeen; got != demofapiSrv.Listener.Addr().String()+"/fapi/v1/balance" {
		t.Fatalf("demofapi 应剥离 /demofapi 前缀且改写 Host，got %q", got)
	}
}

// 令牌错 → 403，且绝不转发到上游。
func TestWrongTokenRejected(t *testing.T) {
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer up.Close()
	cfg := Config{Upstreams: []Upstream{{Prefix: "/api", Target: up.URL}}}
	h, err := newHandler("right", cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v3/ping", nil)
	req.Header.Set("X-Proxy-Token", "wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || hits != 0 {
		t.Fatalf("错令牌应 403 且不转发，got status=%d hits=%d", resp.StatusCode, hits)
	}
}

// 无匹配前缀 → 404，不转发到任何上游。
func TestNoPrefixMatch404(t *testing.T) {
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer up.Close()
	cfg := Config{Upstreams: []Upstream{{Prefix: "/api", Target: up.URL}}}
	h, err := newHandler("tok", cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/unknown/path", nil)
	req.Header.Set("X-Proxy-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || hits != 0 {
		t.Fatalf("无匹配前缀应 404 且不转发，got status=%d hits=%d", resp.StatusCode, hits)
	}
}

// 默认配置包含 OKX + Gate + Binance 全上游。
func TestDefaultConfigHasAllCEX(t *testing.T) {
	cfg := defaultConfig()
	prefixes := make(map[string]string)
	for _, up := range cfg.Upstreams {
		prefixes[up.Prefix] = up.Target
	}
	if prefixes["/api/v5"] != "https://www.okx.com" {
		t.Errorf("默认配置缺 OKX: got %q", prefixes["/api/v5"])
	}
	if prefixes["/api/v4"] != "https://api.gateio.ws" {
		t.Errorf("默认配置缺 Gate: got %q", prefixes["/api/v4"])
	}
	if prefixes["/api"] != "https://api.binance.com" {
		t.Errorf("默认配置缺 Binance spot: got %q", prefixes["/api"])
	}
	if prefixes["/fapi"] != "https://fapi.binance.com" {
		t.Errorf("默认配置缺 Binance futures: got %q", prefixes["/fapi"])
	}
}
