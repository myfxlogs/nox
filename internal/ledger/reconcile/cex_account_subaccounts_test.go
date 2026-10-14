// cex_account_subaccounts_test.go —— D-363 S4：funding/earn 分派与 USDT-only 锚（httptest）。
package reconcile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestD133_BinanceFundingRemoved D-133 R1 ①：binance 资金钱包官方废除+0182 配置行
// 已删=永久不可达——binance:funding 查询径同死码清退，dispatch fail-closed 零出网；
// okx:funding 在域不受影响（TestCEXAccount_OkxFunding 锚）。
func TestD133_BinanceFundingRemoved(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("binance", "funding", "key", "secret", "", srv.URL+"/api", "", false)
	acct, err := q.Account(context.Background())
	if err == nil || acct != nil {
		t.Fatalf("binance funding want unsupported error, got acct=%+v err=%v", acct, err)
	}
	if called {
		t.Error("binance funding 摘除后仍发出 HTTP 请求（应 fail-closed 于 dispatch）")
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

// TestBinanceEarnPositions 财务可见性两件套①：binance:earn 回归（分币口径解除 D-133
// 「只统计 USDT」约束）——签名 GET flexible/position，逐资产聚合分币行：
// 多币 Currency=MULTI；同资产多行合并；零额跳过。
// SG 缺采修：flexible+locked 双端点翻页（size=100）——locked 行额字段 amount。
func TestBinanceEarnPositions(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("X-MBX-APIKEY") == "" {
			t.Error("missing X-MBX-APIKEY")
		}
		if r.URL.Query().Get("size") != "100" || r.URL.Query().Get("current") != "1" {
			t.Errorf("翻页参数缺: %s", r.URL.RawQuery)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/flexible/position"):
			fmt.Fprint(w, `{"total":4,"rows":[
				{"asset":"USDT","totalAmount":"500"},
				{"asset":"USDT","totalAmount":"42"},
				{"asset":"BTC","totalAmount":"0.01"},
				{"asset":"XYZ","totalAmount":"0"}]}`)
		case strings.HasSuffix(r.URL.Path, "/locked/position"):
			// locked 端点额字段=amount（USDT 定期仓位在此——SG 实证主嫌犯径）。
			fmt.Fprint(w, `{"total":1,"rows":[{"asset":"USDT","amount":"1160.69"}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("binance", "earn", "key", "secret", "", srv.URL+"/fapi", "", false)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || !strings.HasSuffix(paths[0], "/flexible/position") ||
		!strings.HasSuffix(paths[1], "/locked/position") {
		t.Fatalf("paths=%v", paths)
	}
	if acct.Currency != "MULTI" || acct.Equity != 1702.7 {
		t.Fatalf("acct=%+v", acct)
	}
	per := map[string]float64{}
	for _, h := range acct.Holdings {
		per[h.Ccy] = h.Qty
	}
	if per["USDT"] != 1702.69 || per["BTC"] != 0.01 || len(per) != 2 {
		t.Fatalf("holdings=%+v", per)
	}
}
