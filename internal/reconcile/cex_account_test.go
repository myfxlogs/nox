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

// TestCEXAccount_Binance：签名余额查询 → D-364 v2 权益=USDT 标 qty 和（非 USDT 标不读取，
// 不发 ticker）；holdings 只含 USDT 标行。httptest 模拟 /api/v3/account（无签名验证）。
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
	// D-364 v2：equity 只计 USDT 标=500；BTC 不读取（holdings 无行）。
	if acct.Equity != 500 {
		t.Errorf("equity want 500（USDT only）, got %v", acct.Equity)
	}
	if len(acct.Holdings) != 1 || acct.Holdings[0].Ccy != "USDT" {
		t.Errorf("holdings want 仅 USDT 行, got %+v", acct.Holdings)
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
		fmt.Fprint(w, `{"code":"0","msg":"","data":[{"totalEq":"1234.56","details":[{"ccy":"USDT","eq":"1234.56","cashBal":"1234.56","eqUsd":"1234.56"}]}]}`)
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

// TestCEXAccount_BinanceProxyToken：配置 ARB_BINANCE_PROXY_TOKEN 时，Binance 签名请求
// 带 X-Proxy-Token 头（静态代理防开放中继鉴权，D-055；D-364 v2 起 ticker 不再调用，
// 仅签名 account 请求）。
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
	if len(gotHeaders) < 1 {
		t.Fatalf("应收到 account 请求，got %d", len(gotHeaders))
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

	// spot：base 已含 /api → 期望 /api/v3/account（D-364 v2：非 USDT 标不发 ticker，
	// /api/v3/ticker/price 路径零调用）；equity 只计 USDT=500。
	q := NewCEXAccountQuerier("binance", "spot", "k", "s", "", srv.URL+"/api", "", false)
	acc, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("spot account: %v", err)
	}
	if acc.Equity != 500 {
		t.Errorf("spot equity want 500 (USDT only；ticker 路径断链则 BTC 明细 usdt_value 缺失), got %v", acc.Equity)
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
	for _, want := range []string{"/api/v3/account", "/fapi/v2/account"} {
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

// TestCEXAccount_BinanceEarnLD D-364 v2 S1/S4：非 USDT 标资产不读取不统计——equity=USDT 标和
// （裸 USDT+LDUSDT 1:1）、holdings 只 2 行 USDT 标；fake ticker 断言零调用（「不读取」实证
// =杂币连定价请求都不发，~90 个 LD* 每轮省调用）。红锚=v1 全计价实现（equity 20536.05+杂币
// 进 holdings+ticker 有调用）。
func TestCEXAccount_BinanceEarnLD(t *testing.T) {
	tickerCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/account":
			fmt.Fprint(w, `{"balances":[
				{"asset":"USDT","free":"0.40","locked":"0"},
				{"asset":"LDUSDT","free":"20225","locked":"0"},
				{"asset":"LDUSDC","free":"18.65","locked":"0"},
				{"asset":"LDTRX","free":"1000","locked":"0"},
				{"asset":"LD未知","free":"5","locked":"0"},
				{"asset":"LDO","free":"10","locked":"0"}
			]}`)
		case "/api/v3/ticker/price":
			tickerCalls++
			fmt.Fprint(w, `{"price":"1.0"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("binance", "spot", "key", "secret", "", srv.URL+"/api", "", false)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("binance account: %v", err)
	}
	// D-364 v2：equity=裸 USDT 0.40+LDUSDT 20225（1:1）=20225.40；红锚=v1 全计价 20536.05。
	want := 0.40 + 20225
	if acct.Equity < want-0.0001 || acct.Equity > want+0.0001 {
		t.Errorf("equity want %.4f, got %.4f（v1 全计价=20536.05 含杂币）", want, acct.Equity)
	}
	// holdings 只 2 行 USDT 标；杂币（LDUSDC/LDTRX/LDO/LD未知）不读取不进明细
	if len(acct.Holdings) != 2 {
		t.Fatalf("holdings want 2 行 USDT 标, got %d: %+v", len(acct.Holdings), acct.Holdings)
	}
	for _, h := range acct.Holdings {
		if h.Ccy != "USDT" && h.Ccy != "LDUSDT" {
			t.Errorf("holdings 含非 USDT 标 %q（v2 不读取）", h.Ccy)
		}
		if h.UsdtValue != h.Qty {
			t.Errorf("USDT 标 1:1: %s usdt_value=%v qty=%v", h.Ccy, h.UsdtValue, h.Qty)
		}
	}
	if tickerCalls != 0 {
		t.Errorf("ticker 调用 = %d, want 0（非 USDT 标不发定价请求=不读取实证）", tickerCalls)
	}
}

// TestCEXAccount_OKXUSDTOnly D-364 S2：okx equity 只计 USDT 标——eq=Σ details[USDT].EqUsd
// （USDT eqUsd=本币 1:1）；totalEq（全币总权益）不再采用。红锚=旧 TotalEq 实现（含 DOGE/
// PENGU 杂币折算）。details 无 USDT 行 → equity=0 不 panic。
func TestCEXAccount_OKXUSDTOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/balance" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"code":"0","msg":"","data":[{"totalEq":"52.24","details":[
			{"ccy":"USDT","eq":"6.24","cashBal":"6.24","eqUsd":"6.24"},
			{"ccy":"DOGE","eq":"25.13","cashBal":"10000","eqUsd":"25.13"},
			{"ccy":"PENGU","eq":"16.95","cashBal":"50000","eqUsd":"16.95"}
		]}]}`)
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("okx", "spot", "key", "secret", "pass", srv.URL, "", false)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("okx account: %v", err)
	}
	if acct.Equity < 6.2399 || acct.Equity > 6.2401 {
		t.Errorf("equity want 6.24（USDT only）, got %v（旧 TotalEq=52.24 含杂币）", acct.Equity)
	}
	// D-364 v2 S2：holdings 只 USDT 行（非 USDT 标不读取）
	if len(acct.Holdings) != 1 || acct.Holdings[0].Ccy != "USDT" {
		t.Errorf("holdings want 仅 USDT 行, got %+v", acct.Holdings)
	}
}
