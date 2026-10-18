package main

// live_equity_map_test.go —— NAV 日切 liveMap 指纹注血修复锚：
// ①MULTI 户分币键=Holdings 原生量、Σqty 指纹永不在任何键值中；
// ②无 Holdings 账户落 venue:type:Currency 单键；
// ③nil/空币名守卫。
import (
	"testing"

	v1 "nox/internal/gen/dashboardv1"
)

func TestLiveEquityMapFromAccounts(t *testing.T) {
	m := liveEquityMapFromAccounts([]*v1.AccountInfo{
		nil,
		{ // binance:earn MULTI 户——Equity=Σqty 指纹（禁入 map）
			Venue: "binance", VenueType: "earn",
			Balance: 101524.64, Equity: 101524.64, Currency: "MULTI",
			Holdings: []*v1.Holding{
				{Ccy: "USDT", Qty: 1592.5},
				{Ccy: "DOGE", Qty: 52886},
				nil, {Ccy: "", Qty: 9},
			},
		},
		{ // binance:perp 单币无 holdings——Currency 键
			Venue: "binance", VenueType: "perp",
			Equity: 1344.2, Currency: "USDT",
		},
		{ // MT 户无 holdings
			Venue: "15032313", VenueType: "mt5",
			Equity: 1000.0, Currency: "USD",
		},
		{ // Currency 空账户不产键
			Venue: "ghost", VenueType: "cex", Equity: 7,
		},
	})
	// MULTI 分币键=原生量
	if got := m["binance:earn:USDT"]; got != 1592.5 {
		t.Fatalf("binance:earn:USDT=%v want 1592.5", got)
	}
	if got := m["binance:earn:DOGE"]; got != 52886 {
		t.Fatalf("binance:earn:DOGE=%v want 52886", got)
	}
	// 指纹值 101524.64 绝不出现在任何键值
	for k, v := range m {
		if v == 101524.64 {
			t.Fatalf("Σqty 指纹泄漏进 map 键 %s", k)
		}
	}
	// 聚合键不存在（venue:type 无 ccy 尾段）
	if _, ok := m["binance:earn"]; ok {
		t.Fatal("MULTI 户聚合键不应存在")
	}
	// 无 holdings 账户=venue:type:Currency 单键
	if got := m["binance:perp:USDT"]; got != 1344.2 {
		t.Fatalf("binance:perp:USDT=%v want 1344.2", got)
	}
	if got := m["15032313:mt5:USD"]; got != 1000 {
		t.Fatalf("mt5 键=%v want 1000", got)
	}
	// 空币名/空 ccy holdings 不产键
	if _, ok := m["binance:earn:"]; ok {
		t.Fatal("空币名键不应存在")
	}
	if _, ok := m["ghost:cex:"]; ok {
		t.Fatal("空 Currency 账户不应产键")
	}
}
