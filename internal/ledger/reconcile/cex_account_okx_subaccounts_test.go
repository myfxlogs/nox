// cex_account_okx_subaccounts_test.go —— okx funding/earn 子账户锚（自
// cex_account_subaccounts_test.go 按 venue 随迁——check-lines 拆分，零语义变化）。
package reconcile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
	if gotPath != "/api/v5/asset/balances" || gotQuery != "" {
		t.Errorf("request = %s?%s, want /api/v5/asset/balances（全币枚举）", gotPath, gotQuery)
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
		switch r.URL.Path {
		case "/api/v5/finance/savings/balance":
			gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
			fmt.Fprint(w, `{"code":"0","data":[{"amt":"5261.25174278","ccy":"USDT","earnings":"69.64","rate":"0.01"}]}`)
		case "/api/v5/finance/sfp/dcd/orders":
			fmt.Fprint(w, `{"code":"0","data":[]}`) // DCI 零在仓单（不扰 equity 断言）
		case "/api/v5/finance/sfp/orders":
			fmt.Fprint(w, `{"code":"0","data":[]}`) // D-537：sfp 结构化零在仓
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
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
