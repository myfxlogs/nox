package reconcile

import (
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nox/internal/platform/httpx"
)

// TestCEXAccount_Binance：签名余额查询 → D-528 分币全枚举——BTC 计价入 equity、
// TRX 无 USDT 对留行不估价（UsdtValue=0 如实）。httptest 模拟 /api/v3/account
// + /v3/ticker/price 全符号表（无签名验证）。
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
			// 全符号表单读（TRXUSDT 缺席=无价——留行 UsdtValue=0）
			fmt.Fprint(w, `[{"symbol":"BTCUSDT","price":"60000"}]`)
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
	// equity=USDT 500 + BTC 0.01×60000=1100；TRX 无价不计值但留行。
	if acct.Equity != 1100 {
		t.Errorf("equity want 1100（500+600 BTC 计价）, got %v", acct.Equity)
	}
	per := map[string]float64{}
	for _, h := range acct.Holdings {
		per[h.Ccy] = h.Qty
	}
	if len(acct.Holdings) != 3 || per["USDT"] != 500 || per["BTC"] != 0.01 || per["TRX"] != 1000 {
		t.Errorf("holdings want USDT+BTC+TRX 三行, got %+v", acct.Holdings)
	}
	if acct.FreeMargin != 500 || acct.FreeQtyByCcy["BTC"] != 0.01 {
		t.Errorf("free want USDT=500 BTC=0.01, got %v %+v", acct.FreeMargin, acct.FreeQtyByCcy)
	}
	if acct.VenueKey != "binance:spot" || acct.AssetType != "cex" || acct.Currency != "USDT" {
		t.Errorf("meta mismatch: %+v", acct)
	}
}

// TestCEXAccount_BinancePerp：合约权益=totalMarginBalance（wb+UPL，D-142；wb≠mb 锚死取源）。
// 实施修正② 复核实测：demo key 对 demo-fapi.binance.com USDT-M 合约同 key 有效（-1109 已解），
// perp 走 /fapi/v2/account 而非 spot 的 /api/v3/account。
// 禁区锚：FreeMargin=availableBalance、buffer=marginBalance/maintMargin 不变。
func TestCEXAccount_BinancePerp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v2/account" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"totalWalletBalance":"5000.00000000","totalMarginBalance":"5100.00000000","totalMaintMargin":"102.00000000","availableBalance":"4900.00000000","totalUnrealizedProfit":"100.00000000","canTrade":true}`)
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("binance", "perp", "key", "secret", "", srv.URL+"/fapi", "", false)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("binance perp account: %v", err)
	}
	if acct.Equity != 5100 {
		t.Errorf("equity want 5100（totalMarginBalance=wb+UPL）, got %v", acct.Equity)
	}
	if acct.Balance != 5000 {
		t.Errorf("balance want 5000（totalWalletBalance）, got %v", acct.Balance)
	}
	if acct.FreeMargin != 4900 {
		t.Errorf("freeMargin want 4900（availableBalance 不变）, got %v", acct.FreeMargin)
	}
	if math.Abs(acct.BufferMultiple-50) > 1e-9 {
		t.Errorf("buffer want 5100/102=50, got %v", acct.BufferMultiple)
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

// TestCEXAccount_OKXSimulated：D-516 起 simulated=t 不再注入 x-simulated-trading
// （demo 端点支路删除；querier 层 simulated 纯标记位，fail-closed 闸在消费侧）。
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
	if gotSimulated != "" {
		t.Errorf("simulated 请求不应带 x-simulated-trading（D-516 支路删除），got %q", gotSimulated)
	}
}

// TestCEXAccount_BinanceProxyToken：配置 NOX_BINANCE_PROXY_TOKEN 时，Binance 签名请求
// 带 X-Proxy-Token 头（静态代理防开放中继鉴权，D-055；D-364 v2 起 ticker 不再调用，
// 仅签名 account 请求）。
func TestCEXAccount_BinanceProxyToken(t *testing.T) {
	httpx.Configure("", "tok-123", nil)
	defer httpx.Configure("", "", nil)
	var gotHeaders []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = append(gotHeaders, r.Header.Clone())
		switch r.URL.Path {
		case "/api/v3/account":
			fmt.Fprint(w, `{"balances":[{"asset":"USDT","free":"500","locked":"0"},{"asset":"BTC","free":"0.01","locked":"0"}]}`)
		case "/api/v3/ticker/price":
			fmt.Fprint(w, `[{"symbol":"BTCUSDT","price":"60000"}]`)
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
// 补齐 gate 账户查询器（此前 Account() switch 无 gate case → 余额条不显示）。
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
// NOX_BINANCE_PROXY_CA 后握手成功——静态 Binance 代理 TLS 链路能建立的关键（D-055）。
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
	httpx.Configure(caFile, "", nil)
	defer httpx.Configure("", "", nil)

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
			fmt.Fprint(w, `[{"symbol":"BTCUSDT","price":"60000"}]`)
		case "/fapi/v2/account":
			fmt.Fprint(w, `{"totalWalletBalance":"1234.5","totalMarginBalance":"1234.5"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// spot：base 已含 /api → 期望 /api/v3/account；D-528 起非 USDT 标计价恢复
	//（ticker/price 全符号单读）——equity=500+0.01×60000=1100。
	q := NewCEXAccountQuerier("binance", "spot", "k", "s", "", srv.URL+"/api", "", false)
	acc, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("spot account: %v", err)
	}
	if acc.Equity != 1100 {
		t.Errorf("spot equity want 1100 (500+BTC 600；D-528 分币计价), got %v", acc.Equity)
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

// TestCEXAccount_BinanceEarnLD LD* 镜像排除锚（LDUSDT 假 free 实证票）：
// LD* 前缀=earn 持仓回显——free/equity 皆不入 spot（earn 池行已记账，入则双计）；
// LDO=Lido DAO 真代币白名单不排（其 earn 镜像应为 LDLDO）。free_margin=裸 USDT free。
func TestCEXAccount_BinanceEarnLD(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/account":
			fmt.Fprint(w, `{"balances":[
				{"asset":"USDT","free":"0.40","locked":"0"},
				{"asset":"LDUSDT","free":"20225","locked":"0"},
				{"asset":"LDUSDC","free":"18.65","locked":"0"},
				{"asset":"LDTRX","free":"1000","locked":"0"},
				{"asset":"LDXX","free":"5","locked":"0"},
				{"asset":"LDO","free":"10","locked":"0"},
				{"asset":"BTC","free":"0.000001","locked":"0"}
			]}`)
		case "/api/v3/ticker/price":
			fmt.Fprint(w, `[{"symbol":"LDOUSDT","price":"1.0"},{"symbol":"BTCUSDT","price":"100000"}]`)
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
	// LD* 全排：equity=USDT 0.40+LDO 10×1.0=10.40；BTC 0.000001×100000=0.1<1 尘埃滤。
	want := 10.40
	if acct.Equity < want-0.0001 || acct.Equity > want+0.0001 {
		t.Errorf("equity want %.4f（LD* 排+LDO 计+尘埃滤）, got %.4f", want, acct.Equity)
	}
	if acct.FreeMargin != 0.40 {
		t.Errorf("freeMargin want 0.40（裸 USDT free——LDUSDT 20225 不得入）, got %v", acct.FreeMargin)
	}
	per := map[string]float64{}
	for _, h := range acct.Holdings {
		per[h.Ccy] = h.Qty
	}
	if len(acct.Holdings) != 2 || per["USDT"] != 0.40 || per["LDO"] != 10 {
		t.Errorf("holdings want USDT+LDO 两行（LD* 排+BTC 尘埃滤）, got %+v", acct.Holdings)
	}
}

// TestCEXAccount_OKXUSDTOnly→D-528 翻转锚：okx equity=Σ details[*].eqUsd
// （venue 权威 USD 总权益——统一账户全币同账）；holdings 全币枚举（尘埃阈
// eqUsd<1 滤行；无价留行 UsdtValue=0）。
func TestCEXAccount_OKXUSDTOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/balance" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"code":"0","msg":"","data":[{"totalEq":"52.24","details":[
			{"ccy":"USDT","eq":"6.24","cashBal":"6.24","eqUsd":"6.24","availBal":"6"},
			{"ccy":"DOGE","eq":"25.13","cashBal":"10000","eqUsd":"25.13","availBal":"9000"},
			{"ccy":"PENGU","eq":"16.95","cashBal":"50000","eqUsd":"16.95","availBal":"50000"},
			{"ccy":"DUST","eq":"0.5","cashBal":"100","eqUsd":"0.5"}
		]}]}`)
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("okx", "spot", "key", "secret", "pass", srv.URL, "", false)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("okx account: %v", err)
	}
	// equity=6.24+25.13+16.95+0.5=48.82（全币 eqUsd 和）；free_margin=USDT availBal=6。
	if acct.Equity < 48.81 || acct.Equity > 48.83 {
		t.Errorf("equity want 48.82（全币 eqUsd）, got %v", acct.Equity)
	}
	if acct.FreeMargin != 6 {
		t.Errorf("freeMargin want 6（USDT availBal）, got %v", acct.FreeMargin)
	}
	per := map[string]float64{}
	for _, h := range acct.Holdings {
		per[h.Ccy] = h.Qty
	}
	if len(acct.Holdings) != 3 || per["USDT"] != 6.24 || per["DOGE"] != 10000 || per["PENGU"] != 50000 {
		t.Errorf("holdings want 3 行（尘埃滤）, got %+v", acct.Holdings)
	}
	if acct.FreeQtyByCcy["DOGE"] != 9000 {
		t.Errorf("FreeQtyByCcy[DOGE] want 9000, got %v", acct.FreeQtyByCcy)
	}
}

// TestCEXAccount_OkxFutures gap#11：okx:futures 配置行走统一账户读径（/api/v5/account/balance
// 同 spot/perp——统一账户同一余额域覆盖交割持仓资金）——不 panic 零 fail-closed；
// venue_type 原值穿透（写侧归一 unified 在 dashboard/cexscan 层，querier 不掺）。
func TestCEXAccount_OkxFutures(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"code":"0","msg":"","data":[{"totalEq":"100","details":[
			{"ccy":"USDT","eq":"100","cashBal":"100","availBal":"80","eqUsd":"100"}
		]}]}`)
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("okx", "futures", "key", "secret", "pass", srv.URL, "", false)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("okx futures account: %v", err)
	}
	if gotPath != "/api/v5/account/balance" {
		t.Errorf("path = %s, want /api/v5/account/balance", gotPath)
	}
	if acct.VenueKey != "okx:futures" || acct.Equity < 99.9 || acct.FreeMargin < 79.9 {
		t.Errorf("acct = %+v", acct)
	}
}
