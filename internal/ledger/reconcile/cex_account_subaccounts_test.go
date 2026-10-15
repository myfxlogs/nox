// cex_account_subaccounts_test.go —— D-363 S4：funding/earn 分派与 USDT-only 锚（httptest）。
// venue 锚随迁同名件：okx→cex_account_okx_subaccounts_test.go、
// gate→cex_account_gate_subaccounts_test.go（check-lines 拆分）。
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
		switch {
		case strings.HasSuffix(r.URL.Path, "/flexible/position"):
			if r.URL.Query().Get("size") != "100" || r.URL.Query().Get("current") != "1" {
				t.Errorf("翻页参数缺: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"total":4,"rows":[
				{"asset":"USDT","totalAmount":"500"},
				{"asset":"USDT","totalAmount":"42"},
				{"asset":"BTC","totalAmount":"0.01"},
				{"asset":"XYZ","totalAmount":"0"}]}`)
		case strings.HasSuffix(r.URL.Path, "/locked/position"):
			if r.URL.Query().Get("size") != "100" || r.URL.Query().Get("current") != "1" {
				t.Errorf("翻页参数缺: %s", r.URL.RawQuery)
			}
			// locked 端点额字段=amount（USDT 定期仓位在此——SG 实证主嫌犯径）。
			fmt.Fprint(w, `{"total":1,"rows":[{"asset":"USDT","amount":"1160.69"}]}`)
		case strings.HasSuffix(r.URL.Path, "/v1/dci/product/positions"):
			// DCI 持仓端点（DCI 在仓入 earn 池径）：fixture 零在仓单不扰对拍。
			fmt.Fprint(w, `{"data":[]}`)
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
	if len(paths) != 3 || !strings.HasSuffix(paths[0], "/flexible/position") ||
		!strings.HasSuffix(paths[1], "/locked/position") ||
		!strings.HasSuffix(paths[2], "/v1/dci/product/positions") {
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
