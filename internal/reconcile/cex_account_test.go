package reconcile

import (
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arb/internal/httpx"
)

// TestCEXAccount_Binance：签名余额查询 → 权益 = Σ 持币×USDT 价（USDT 价=1，其余走公开 ticker/price）。
// 用 httptest 模拟 /api/v3/account 与 /api/v3/ticker/price（无签名验证——本地假服务只看响应解析）。
func TestCEXAccount_Binance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/account":
			fmt.Fprint(w, `{"balances":[
				{"asset":"USDT","free":"500","locked":"0"},
				{"asset":"BTC","free":"0.01","locked":"0"},
				{"asset":"TRX","free":"1000","locked":"0"}
			]}`)
		case "/api/v3/ticker/price":
			if r.URL.Query().Get("symbol") == "BTCUSDT" {
				fmt.Fprint(w, `{"symbol":"BTCUSDT","price":"60000"}`)
				return
			}
			// TRXUSDT 无 USDT 对 → 404，该币跳过（诚实估算）
			http.Error(w, "invalid symbol", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// srv.URL 是 http://host:port；生产 base 已含 /api 前缀（fapi→api 替换后），D-273 S1 契约=代码不再拼前缀。
	q := NewCEXAccountQuerier("binance", "spot", "key", "secret", "", srv.URL+"/api", "", false)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("binance account: %v", err)
	}
	want := 500 + 0.01*60000 // USDT + BTC×价；TRX 无价跳过
	if acct.Equity != want {
		t.Errorf("equity want %v, got %v", want, acct.Equity)
	}
	if acct.VenueKey != "binance:spot" || acct.AssetType != "cex" || acct.Currency != "USDT" {
		t.Errorf("meta mismatch: %+v", acct)
	}
}

// TestCEXAccount_BinancePerp：合约账户权益 = /fapi/v2/account 的 totalWalletBalance（权威 USDT 权益）。
// D-059 实施修正② 复核实测：demo key 对 demo-fapi.binance.com USDT-M 合约同 key 有效（-1109 已解），
// perp 走 /fapi/v2/account 而非 spot 的 /api/v3/account。
func TestCEXAccount_BinancePerp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v2/account" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"totalWalletBalance":"5000.00000000","totalUnrealizedProfit":"0.00000000","canTrade":true}`)
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("binance", "perp", "key", "secret", "", srv.URL+"/fapi", "", false)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("binance perp account: %v", err)
	}
	if acct.Equity != 5000 {
		t.Errorf("equity want 5000, got %v", acct.Equity)
	}
	if acct.VenueKey != "binance:perp" {
		t.Errorf("venuekey want binance:perp, got %q", acct.VenueKey)
	}
}

// TestCEXAccount_OKX：totalEq 权威 USD 总权益 + 签名头存在（base64 HMAC）。
func TestCEXAccount_OKX(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/balance" {
			http.NotFound(w, r)
			return
		}
		gotHeaders = r.Header
		fmt.Fprint(w, `{"code":"0","msg":"","data":[{"totalEq":"1234.56","details":[{"ccy":"USDT","eq":"1234.56"}]}]}`)
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("okx", "spot", "key", "secret", "pass", srv.URL, "", false)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("okx account: %v", err)
	}
	if acct.Equity != 1234.56 {
		t.Errorf("equity want 1234.56, got %v", acct.Equity)
	}
	for _, h := range []string{"OK-ACCESS-KEY", "OK-ACCESS-SIGN", "OK-ACCESS-TIMESTAMP", "OK-ACCESS-PASSPHRASE"} {
		if gotHeaders.Get(h) == "" {
			t.Errorf("missing header %s", h)
		}
	}
	if gotHeaders.Get("x-simulated-trading") != "" {
		t.Errorf("实盘请求不应带 x-simulated-trading")
	}
}

// TestCEXAccount_OKXSimulated：OKX 模拟盘请求必须带 x-simulated-trading:1（demo key 环境匹配，D-054）。
func TestCEXAccount_OKXSimulated(t *testing.T) {
	var gotSimulated string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/balance" {
			http.NotFound(w, r)
			return
		}
		gotSimulated = r.Header.Get("x-simulated-trading")
		fmt.Fprint(w, `{"code":"0","msg":"","data":[{"totalEq":"99.00","details":[{"ccy":"USDT","eq":"99"}]}]}`)
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("okx", "spot", "key", "secret", "pass", srv.URL, "", true)
	if _, err := q.Account(context.Background()); err != nil {
		t.Fatalf("okx simulated account: %v", err)
	}
	if gotSimulated != "1" {
		t.Errorf("simulated 请求应带 x-simulated-trading:1，got %q", gotSimulated)
	}
}

// TestCEXAccount_BinanceProxyToken：配置 ARB_BINANCE_PROXY_TOKEN 时，Binance 签名请求与公开
// ticker 都带 X-Proxy-Token 头（静态代理防开放中继鉴权，D-055）。
func TestCEXAccount_BinanceProxyToken(t *testing.T) {
	t.Setenv("ARB_BINANCE_PROXY_TOKEN", "tok-123")
	var gotHeaders []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = append(gotHeaders, r.Header.Clone())
		switch r.URL.Path {
		case "/api/v3/account":
			fmt.Fprint(w, `{"balances":[{"asset":"USDT","free":"500","locked":"0"},{"asset":"BTC","free":"0.01","locked":"0"}]}`)
		case "/api/v3/ticker/price":
			fmt.Fprint(w, `{"symbol":"BTCUSDT","price":"60000"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("binance", "spot", "key", "secret", "", srv.URL+"/api", "", false)
	if _, err := q.Account(context.Background()); err != nil {
		t.Fatalf("binance account: %v", err)
	}
	if len(gotHeaders) < 2 {
		t.Fatalf("应至少收到 account+ticker 两个请求，got %d", len(gotHeaders))
	}
	for i, h := range gotHeaders {
		if h.Get("X-Proxy-Token") != "tok-123" {
			t.Errorf("请求 %d 缺 X-Proxy-Token，got %q", i, h.Get("X-Proxy-Token"))
		}
	}
}

// TestCEXAccount_GatePerp：gate perp 账户权益 = /futures/usdt/accounts 的 total（USDT 总权益）。
// D-098 G1：补齐 gate 账户查询器（此前 Account() switch 无 gate case → 余额条不显示）。
func TestCEXAccount_GatePerp(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/futures/usdt/accounts" {
			http.NotFound(w, r)
			return
		}
		gotHeaders = r.Header
		fmt.Fprint(w, `{"total":"5000.123456","unrealized_pnl":"0","available":"5000.123456"}`)
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("gate", "perp", "gate-key", "gate-secret", "", srv.URL, "", true)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("gate perp account: %v", err)
	}
	if acct.Equity != 5000.123456 {
		t.Errorf("equity want 5000.123456, got %v", acct.Equity)
	}
	if acct.VenueKey != "gate:perp" || acct.AssetType != "cex" || acct.Currency != "USDT" {
		t.Errorf("meta mismatch: %+v", acct)
	}
	// 签名三件
	for _, h := range []string{"KEY", "SIGN", "Timestamp"} {
		if gotHeaders.Get(h) == "" {
			t.Errorf("missing header %s", h)
		}
	}
}

// TestCEXAccount_GateSpotUnsupported：gate spot（G1 只 perp）→ 如实返回 unsupported error。
func TestCEXAccount_GateSpotUnsupported(t *testing.T) {
	q := NewCEXAccountQuerier("gate", "spot", "k", "s", "", "http://x", "", true)
	if _, err := q.Account(context.Background()); err == nil {
		t.Fatal("gate spot 应报错（G1 仅 perp），got nil")
	}
}

// TestCEXAccount_GatePerpEmptyTotal：total 空值/缺失 → 容错返回 0（不 panic）。
func TestCEXAccount_GatePerpEmptyTotal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"total":"","unrealized_pnl":""}`)
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("gate", "perp", "k", "s", "", srv.URL, "", true)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("空 total 应容错不报错: %v", err)
	}
	if acct.Equity != 0 {
		t.Errorf("空 total equity want 0, got %v", acct.Equity)
	}
}

// TestHttpxClientTrustsProxyCA：默认客户端不信任自签代理证书（握手失败）；加载
// ARB_BINANCE_PROXY_CA 后握手成功——静态 Binance 代理 TLS 链路能建立的关键（D-055）。
func TestHttpxClientTrustsProxyCA(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "pong")
	}))
	defer ts.Close()

	if _, err := (&http.Client{Timeout: 3 * time.Second}).Get(ts.URL); err == nil {
		t.Fatal("默认客户端不应信任 httptest 自签证书")
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARB_BINANCE_PROXY_CA", caFile)

	resp, err := httpx.Client().Get(ts.URL)
	if err != nil {
		t.Fatalf("加载 CA 后应能握手: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status want 200, got %d", resp.StatusCode)
	}
}

// TestCEXAccount_BinancePathNoDoublePrefix（D-273 S1）：base URL 已含 /api 或 /fapi 时，
// 代码不得再拼一次前缀（双 /api //fapi 是生产 404 根因）。断言实际请求路径与总权益。
func TestCEXAccount_BinancePathNoDoublePrefix(t *testing.T) {
	var mu []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu = append(mu, r.URL.Path)
		switch r.URL.Path {
		case "/api/v3/account":
			fmt.Fprint(w, `{"balances":[{"asset":"USDT","free":"500","locked":"0"},{"asset":"BTC","free":"0.01","locked":"0"}]}`)
		case "/api/v3/ticker/price":
			fmt.Fprint(w, `{"price":"60000"}`)
		case "/fapi/v2/account":
			fmt.Fprint(w, `{"totalWalletBalance":"1234.5"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// spot：base 已含 /api → 期望 /api/v3/account + /api/v3/ticker/price，BTC 计入权益=500+600
	q := NewCEXAccountQuerier("binance", "spot", "k", "s", "", srv.URL+"/api", "", false)
	acc, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("spot account: %v", err)
	}
	if acc.Equity != 1100 {
		t.Errorf("spot equity want 1100 (500 USDT + 0.01 BTC×60000), got %v（ticker 路径断链则 BTC 被跳过）", acc.Equity)
	}
	// perp：base 已含 /fapi → 期望 /fapi/v2/account
	pq := NewCEXAccountQuerier("binance", "perp", "k", "s", "", srv.URL+"/fapi", "", false)
	pacc, err := pq.Account(context.Background())
	if err != nil {
		t.Fatalf("perp account: %v", err)
	}
	if pacc.Equity != 1234.5 {
		t.Errorf("perp equity want 1234.5, got %v", pacc.Equity)
	}
	// 请求路径断言：任一请求路径不得含双前缀 /api/api 或 /fapi/fapi
	for _, p := range mu {
		if strings.Contains(p, "/api/api") || strings.Contains(p, "/fapi/fapi") {
			t.Errorf("double-prefix path: %s", p)
		}
	}
	for _, want := range []string{"/api/v3/account", "/api/v3/ticker/price", "/fapi/v2/account"} {
		found := false
		for _, p := range mu {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected request path %s not seen (got %v)", want, mu)
		}
	}
}
