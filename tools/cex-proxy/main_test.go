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
	// 故意乱序排列——最长前缀匹配必须自行排序，不依赖配置顺序。
	cfg := Config{Upstreams: []Upstream{
		{Prefix: "/api", Target: apiSrv.URL},
		{Prefix: "/api/v5", Target: okxSrv.URL},
		{Prefix: "/fapi", Target: fapiSrv.URL},
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

	// 6./7. D-516：demo 三路由删除——/demo、/demofapi 无前缀命中 → 404（无匹配兜底）。
	for _, p := range []string{"/demo/api/v3/account", "/demofapi/fapi/v1/balance"} {
		req, _ = http.NewRequest(http.MethodGet, ts.URL+p, nil)
		req.Header.Set("X-Proxy-Token", "secret-token")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s 应 404（demo 路由已摘 D-516），got %d", p, resp.StatusCode)
		}
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

// WS 路由断言（D-450）：四条 WS 前缀命中正确上游，strip 行为正确。
func TestWSRoutePrefixes(t *testing.T) {
	type hit struct{ host, path string }
	seen := make(map[string]chan hit)
	srv := func(name string) *httptest.Server {
		ch := make(chan hit, 1)
		seen[name] = ch
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ch <- hit{r.Host, r.URL.Path}
			io.WriteString(w, name)
		}))
	}
	gateWS := srv("gatews")
	fstream := srv("fstream")
	spotWS := srv("spotws")
	wsapi := srv("wsapi")
	defer gateWS.Close()
	defer fstream.Close()
	defer spotWS.Close()
	defer wsapi.Close()

	cfg := Config{Upstreams: []Upstream{
		{Prefix: "/ws", Target: fstream.URL},
		{Prefix: "/v4/ws", Target: gateWS.URL},
		{Prefix: "/public", Target: fstream.URL},
		{Prefix: "/market", Target: fstream.URL},
		{Prefix: "/private", Target: fstream.URL},
		{Prefix: "/spotws", Target: spotWS.URL, StripPrefix: true},
		{Prefix: "/wsapi", Target: wsapi.URL, StripPrefix: true},
		{Prefix: "/api", Target: fstream.URL}, // 干扰项：/ws 不得被 /api 抢
	}}
	h, err := newHandler("tok", cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	get := func(path string) string {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("X-Proxy-Token", "tok")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(body)
	}

	// gate WS：不 strip，上游收到原路径 /v4/ws/usdt。
	if got := get("/v4/ws/usdt"); got != "gatews" {
		t.Fatalf("/v4/ws 应命中 gate WS 上游，got %q", got)
	}
	if h := <-seen["gatews"]; h.path != "/v4/ws/usdt" {
		t.Fatalf("gate WS 不应 strip，got path=%q", h.path)
	}
	// binance 合约用户流：/ws/<key> → fstream 不 strip。
	if got := get("/ws/abc123key"); got != "fstream" {
		t.Fatalf("/ws 应命中 fstream 上游，got %q", got)
	}
	if h := <-seen["fstream"]; h.path != "/ws/abc123key" {
		t.Fatalf("fstream 不应 strip，got path=%q", h.path)
	}
	// 三路由：/public /market /private → fstream 不 strip（D-516：demofws 删除）。
	for _, p := range []string{"/public/ws", "/market/ws", "/private/ws/keyZ"} {
		if got := get(p); got != "fstream" {
			t.Fatalf("%s 应命中 fstream 上游，got %q", p, got)
		}
		if h := <-seen["fstream"]; h.path != p {
			t.Fatalf("%s 不应 strip，got path=%q", p, h.path)
		}
	}
	// spot 用户流：strip /spotws → /ws/<key>。
	if got := get("/spotws/ws/keyY"); got != "spotws" {
		t.Fatalf("/spotws 应命中 spot stream，got %q", got)
	}
	if h := <-seen["spotws"]; h.path != "/ws/keyY" {
		t.Fatalf("spotws 应 strip 为 /ws/<key>，got path=%q", h.path)
	}
	// spot 用户流 ws-api 控制面：strip /wsapi → /ws-api/v3。
	if got := get("/wsapi/ws-api/v3"); got != "wsapi" {
		t.Fatalf("/wsapi 应命中 ws-api 上游，got %q", got)
	}
	if h := <-seen["wsapi"]; h.path != "/ws-api/v3" {
		t.Fatalf("wsapi 应 strip 为 /ws-api/v3，got path=%q", h.path)
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
	for p, want := range map[string]string{
		"/v4/ws":   "https://fx-ws.gateio.ws",
		"/ws":      "https://fstream.binance.com",
		"/public":  "https://fstream.binance.com",
		"/market":  "https://fstream.binance.com",
		"/private": "https://fstream.binance.com",
		"/spotws":  "https://stream.binance.com:9443",
		"/wsapi":   "https://ws-api.binance.com",
	} {
		if prefixes[p] != want {
			t.Errorf("默认配置缺 WS 路由 %s: got %q", p, prefixes[p])
		}
	}
}
