// cex_account_full_inventory_test.go 附属锚（D-365-R1 S2）。
package reconcile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFullInventory_BinancePerpWallet R1 S2 锚：perp 配置走 /fapi/v2/account 合约钱包
// （totalWalletBalance），不是 spot /v3/account。
func TestFullInventory_BinancePerpWallet(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"totalWalletBalance":"345.74"}`)
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

// TestFullInventory_BinanceFundingPriced R1 S2 锚：funding 全量资产带价（DOGE 52,883 在
// 资金钱包——核证点④），USDT 行计 usdt_equity，DOGE/USDC 计价进 holdings/inventory 不进
// equity； sapi 与 ticker 同源单 server（base 无 /api 后缀 → sapi 与 /v3 同根）。
func TestFullInventory_BinanceFundingPriced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sapi/v1/asset/get-funding-asset":
			fmt.Fprint(w, `[
				{"asset":"DOGE","free":"52883.26","locked":"0"},
				{"asset":"USDC","free":"445","locked":"0"},
				{"asset":"USDT","free":"0","locked":"0"}
			]`)
		case "/v3/ticker/price":
			switch r.URL.Query().Get("symbol") {
			case "DOGEUSDT":
				fmt.Fprint(w, `{"price":"0.1"}`)
			case "USDCUSDT":
				fmt.Fprint(w, `{"price":"1.0"}`)
			default:
				http.Error(w, "no pair", http.StatusBadRequest)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("binance", "funding", "k", "s", "", srv.URL, "", false)
	inv, err := q.FullInventory(context.Background())
	if err != nil {
		t.Fatalf("funding full inventory: %v", err)
	}
	if inv.UsdtEquity != 0 {
		t.Errorf("usdt_equity = %v, want 0（funding 钱包零 USDT 真探实证）", inv.UsdtEquity)
	}
	want := map[string]float64{"DOGE": 52883.26 * 0.1, "USDC": 445} // USDT 行 qty=0 → 不入明细
	if len(inv.Holdings) != len(want) {
		t.Fatalf("holdings = %d 行, want %d（全币种盘点）: %+v", len(inv.Holdings), len(want), inv.Holdings)
	}
	for _, h := range inv.Holdings {
		wv, ok := want[h.Ccy]
		if !ok {
			t.Errorf("意外币种 %q", h.Ccy)
			continue
		}
		if h.UsdtValue < wv-0.01 || h.UsdtValue > wv+0.01 {
			t.Errorf("holdings %s usdt_value = %v, want %v", h.Ccy, h.UsdtValue, wv)
		}
	}
	total := 0.0
	for _, v := range want {
		total += v
	}
	if inv.InventoryUSD < total-0.01 || inv.InventoryUSD > total+0.01 {
		t.Errorf("inventory = %v, want %v（全币种计价和）", inv.InventoryUSD, total)
	}
}
