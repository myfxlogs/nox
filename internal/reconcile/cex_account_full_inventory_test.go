// cex_account_full_inventory_test.go 附属锚（D-365-R1 S2）。
package reconcile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	v1 "arb/internal/gen/dashboardv1"
)

// TestFullInventory_BinancePerpWallet R1 S2 锚：perp 配置走 /fapi/v2/account 合约钱包
// （totalWalletBalance），不是 spot /v3/account。
func TestFullInventory_BinancePerpWallet(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"totalWalletBalance":"345.74","totalMarginBalance":"345.74"}`)
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("binance", "perp", "k", "s", "", srv.URL+"/fapi", "", false)
	inv, err := q.FullInventory(context.Background())
	if err != nil {
		t.Fatalf("perp full inventory: %v", err)
	}
	if gotPath != "/fapi/v2/account" {
		t.Errorf("path = %q, want /fapi/v2/account（perp 钱包非 spot）", gotPath)
	}
	if inv.UsdtEquity != 345.74 || inv.InventoryUSD != 345.74 {
		t.Errorf("usdt_equity/inventory = %v/%v, want 345.74", inv.UsdtEquity, inv.InventoryUSD)
	}
}

// TestFullInventory_BinanceFundingRemoved D-133 R1 ①：binance:funding 全币种盘点径
// 摘除——unsupported fail-closed 零出网（禁落 binance spot 分支冒充现货盘点）。
func TestFullInventory_BinanceFundingRemoved(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("binance", "funding", "k", "s", "", srv.URL, "", false)
	inv, err := q.FullInventory(context.Background())
	if err == nil || inv != nil {
		t.Fatalf("binance funding FullInventory want unsupported error, got inv=%+v err=%v", inv, err)
	}
	if called {
		t.Error("binance funding 摘除后仍发出 HTTP 请求（应 fail-closed 于 dispatch）")
	}
}

// TestFullInventory_OkxTradingQty D-365-R3：trading details 的 Holding.Qty=eq（ccy 本位
// 真数量），UsdtValue=eqUsd——R0 缺陷曾把 eqUsd 美元值塞 Qty（同类不一致，杂币持有时
// 明细数量假）。红=R0 实现 Qty=88.7。
func TestFullInventory_OkxTradingQty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/balance" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"code":"0","data":[{"details":[
			{"ccy":"DOGE","eq":"1000","eqUsd":"88.7"},
			{"ccy":"USDT","eq":"6.24","eqUsd":"6.24"}
		]}]}`)
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("okx", "spot", "k", "s", "p", srv.URL, "", false)
	inv, err := q.FullInventory(context.Background())
	if err != nil {
		t.Fatalf("okx trading full inventory: %v", err)
	}
	var doge *v1.Holding
	for _, h := range inv.Holdings {
		if h.Ccy == "DOGE" {
			doge = h
		}
	}
	if doge == nil {
		t.Fatalf("holdings 缺 DOGE: %+v", inv.Holdings)
	}
	if doge.Qty != 1000 {
		t.Errorf("DOGE Qty = %v, want 1000（本位真数量，R0 曾塞 eqUsd=88.7）", doge.Qty)
	}
	if doge.UsdtValue < 88.6999 || doge.UsdtValue > 88.7001 {
		t.Errorf("DOGE UsdtValue = %v, want 88.7", doge.UsdtValue)
	}
}
