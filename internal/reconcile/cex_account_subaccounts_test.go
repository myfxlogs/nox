// cex_account_subaccounts_test.go —— D-363 S4：funding/earn 分派与 USDT-only 锚（httptest）。
package reconcile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCEXAccount_BinanceFunding D-363 S2：binance funding 走根路径 /sapi/v1/asset/
// get-funding-asset（POST），多资产响应只取 USDT 行（DOGE/USDC 不读取）。
func TestCEXAccount_BinanceFunding(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `[
			{"asset":"DOGE","free":"52883.26","locked":"0"},
			{"asset":"USDC","free":"445","locked":"0"},
			{"asset":"USDT","free":"12.5","locked":"0.5"}
		]`)
	}))
	defer srv.Close()

	// base 带 /api 后缀 → 应剥后走根路径（核证点④：/api 前缀 sapi 404）
	q := NewCEXAccountQuerier("binance", "funding", "key", "secret", "", srv.URL+"/api", "", false)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("binance funding: %v", err)
	}
	if gotPath != "/sapi/v1/asset/get-funding-asset" {
		t.Errorf("request path = %q, want /sapi/v1/asset/get-funding-asset（根路径）", gotPath)
	}
	if acct.Equity != 13.0 {
		t.Errorf("equity want 13.0（USDT free+locked）, got %v", acct.Equity)
	}
	if len(acct.Holdings) != 1 || acct.Holdings[0].Ccy != "USDT" {
		t.Errorf("holdings want 仅 USDT 行, got %+v", acct.Holdings)
	}
	if acct.VenueKey != "binance:funding" || acct.AssetType != "cex" || acct.Currency != "USDT" {
		t.Errorf("meta mismatch: %+v", acct)
	}
}

// TestCEXAccount_OkxFunding D-363 S2：okx funding 走 /api/v5/asset/balances?ccy=USDT
// （直连，代理 403 核证点④）；真探实证 USDT 0 → equity=0（资金账户空）。
func TestCEXAccount_OkxFunding(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		fmt.Fprint(w, `{"code":"0","data":[{"availBal":"0","bal":"0","ccy":"USDT","frozenBal":"0"}]}`)
	}))
	defer srv.Close()

	okxSubaccountBase = srv.URL // 测试注入（生产默认 www.okx.com 直连）
	q := NewCEXAccountQuerier("okx", "funding", "key", "secret", "pass", srv.URL, "", false)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("okx funding: %v", err)
	}
	if gotPath != "/api/v5/asset/balances" || gotQuery != "ccy=USDT" {
		t.Errorf("request = %s?%s, want /api/v5/asset/balances?ccy=USDT", gotPath, gotQuery)
	}
	if acct.Equity != 0 {
		t.Errorf("equity want 0（真探实证资金空）, got %v", acct.Equity)
	}
	if acct.VenueKey != "okx:funding" {
		t.Errorf("venuekey want okx:funding, got %q", acct.VenueKey)
	}
}

// TestCEXAccount_OkxSubaccountPrefersConfigBase 9-22 回归锚：config base_url（cex-proxy）
// 优先于直连兜底 okxSubaccountBase——9-20「代理不路由」系 CF 1010 误诊，直连会被主机
// 代理分流到轮换出口 IP → OKX 白名单 50110。
func TestCEXAccount_OkxSubaccountPrefersConfigBase(t *testing.T) {
	hitProxy, hitFallback := false, false
	srvProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitProxy = true
		fmt.Fprint(w, `{"code":"0","data":[{"availBal":"0","bal":"0","ccy":"USDT","frozenBal":"0"}]}`)
	}))
	defer srvProxy.Close()
	srvFallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitFallback = true
		fmt.Fprint(w, `{"code":"0","data":[{"availBal":"0","bal":"0","ccy":"USDT","frozenBal":"0"}]}`)
	}))
	defer srvFallback.Close()

	old := okxSubaccountBase
	okxSubaccountBase = srvFallback.URL
	defer func() { okxSubaccountBase = old }()

	q := NewCEXAccountQuerier("okx", "funding", "key", "secret", "pass", srvProxy.URL, "", false)
	if _, err := q.Account(context.Background()); err != nil {
		t.Fatalf("account: %v", err)
	}
	if !hitProxy || hitFallback {
		t.Errorf("base_url 优先断言失败: hitProxy=%v hitFallback=%v", hitProxy, hitFallback)
	}

	// 兜底分支：base_url 空 → 回落 okxSubaccountBase
	hitProxy, hitFallback = false, false
	q2 := NewCEXAccountQuerier("okx", "funding", "key", "secret", "pass", "", "", false)
	if _, err := q2.Account(context.Background()); err != nil {
		t.Fatalf("account fallback: %v", err)
	}
	if !hitFallback {
		t.Errorf("空 base_url 应回落直连兜底, hitFallback=%v", hitFallback)
	}
}

// TestCEXAccount_OkxEarn D-363 S2：okx earn 走 /api/v5/finance/savings/balance?ccy=USDT
// → equity=amt（真探实证 5261.25）；holdings 单行 USDT。
func TestCEXAccount_OkxEarn(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		fmt.Fprint(w, `{"code":"0","data":[{"amt":"5261.25174278","ccy":"USDT","earnings":"69.64","rate":"0.01"}]}`)
	}))
	defer srv.Close()

	okxSubaccountBase = srv.URL // 测试注入（生产默认 www.okx.com 直连）
	q := NewCEXAccountQuerier("okx", "earn", "key", "secret", "pass", srv.URL, "", false)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("okx earn: %v", err)
	}
	if gotPath != "/api/v5/finance/savings/balance" || gotQuery != "ccy=USDT" {
		t.Errorf("request = %s?%s, want /api/v5/finance/savings/balance?ccy=USDT", gotPath, gotQuery)
	}
	if acct.Equity < 5261.25 || acct.Equity > 5261.26 {
		t.Errorf("equity want ~5261.25（真探实证）, got %v", acct.Equity)
	}
	if len(acct.Holdings) != 1 || acct.Holdings[0].Ccy != "USDT" || acct.Holdings[0].Qty < 5261.25 {
		t.Errorf("holdings want 单行 USDT 5261.25, got %+v", acct.Holdings)
	}
}

// TestCEXAccount_BinanceEarnOmitted D-363 核证点②：binance locked 零仓（实探 total=0）→
// earn 分派省略报错（行缺省不列）。
func TestCEXAccount_BinanceEarnOmitted(t *testing.T) {
	q := NewCEXAccountQuerier("binance", "earn", "k", "s", "", "http://x", "", false)
	if _, err := q.Account(context.Background()); err == nil {
		t.Fatal("binance earn 应省略报错（locked 零仓），got nil")
	}
}
