package reconcile

// okx/gate 钱包面响应→三档分类 fixture 锚（availEq=立即可用/frozenBal=锁定/
// savings=可转可用/gate spot+futures 分派不 panic）。

import (
	"context"
	"testing"
)

// T4① okx trading：availEq→立即可用，availBal−availEq+frozenBal→锁定慢动；非 USDT 按 eqUsd/eq 比折价。
func TestD547_OkxTradingTiers(t *testing.T) {
	body := []byte(`{"code":"0","msg":"","data":[{"details":[
		{"ccy":"USDT","eq":"100","eqUsd":"100","availEq":"80","availBal":"90","frozenBal":"10"},
		{"ccy":"BTC","eq":"0.1","eqUsd":"1000","availEq":"0.05","availBal":"0.06","frozenBal":"0.01"},
		{"ccy":"ZZZ","eq":"0","eqUsd":"0","availEq":"9","availBal":"9","frozenBal":"9"}
	]}]}`)
	n, err := okxTradingNode(body)
	if err != nil {
		t.Fatalf("trading node: %v", err)
	}
	// USDT: availEq 80 + BTC: 0.05×10000=500 → 580。
	if want := 580.0; n.AvailableUSD != want {
		t.Fatalf("available=%v want %v", n.AvailableUSD, want)
	}
	// USDT: (90−80+10)=20 + BTC: (0.06−0.05+0.01)×10000=200 → 220；eqUsd=0 行跳过不 panic。
	if want := 220.0; n.LockedUSD < want-0.001 || n.LockedUSD > want+0.001 {
		t.Fatalf("locked=%v want %v", n.LockedUSD, want)
	}
}

// T4② okx 子面：funding availBal/earn amt→可转可用档；USDT 直计。
func TestD547_OkxSubTiers(t *testing.T) {
	q := NewCEXAccountQuerier("okx", "perp", "k", "s", "p", "https://x", "", false)
	n, err := q.okxSubNode(context.Background(), "earn", "amt",
		[]byte(`{"code":"0","data":[{"ccy":"USDT","amt":"300"},{"ccy":"ZZZ","amt":"9"}]}`))
	if err != nil {
		t.Fatalf("sub node: %v", err)
	}
	if n.TransferableUSD != 300 { // ZZZ 折价失败→Detail 注记不算入（不猜价）
		t.Fatalf("transferable=%v detail=%q", n.TransferableUSD, n.Detail)
	}
	if n.AvailableUSD != 0 || n.LockedUSD != 0 {
		t.Fatalf("子面应纯可转可用: %+v", n)
	}
}

// T4③ gate spot：available→立即可用、locked→锁定慢动；futures：available/position_margin+order_margin。
func TestD547_GateTiers(t *testing.T) {
	q := NewCEXAccountQuerier("gate", "perp", "k", "s", "", "https://x", "", false)
	sp, err := q.gateSpotNode(context.Background(),
		[]byte(`[{"currency":"USDT","available":"50","locked":"5"},{"currency":"BTC","available":"0","locked":"0"},{"currency":"ETH","available":"1","locked":"0"}]`),
		func(context.Context, string) (float64, error) { return 2000, nil })
	if err != nil {
		t.Fatalf("spot node: %v", err)
	}
	if sp.AvailableUSD != 2050 || sp.LockedUSD != 5 {
		t.Fatalf("spot: %+v", sp)
	}
	fu, err := gateFuturesNode([]byte(`{"total":"1000","available":"700","position_margin":"250","order_margin":"50"}`))
	if err != nil {
		t.Fatalf("futures node: %v", err)
	}
	if fu.AvailableUSD != 700 || fu.LockedUSD != 300 {
		t.Fatalf("futures: %+v", fu)
	}
	// 空/畸形响应不 panic。
	if _, err := gateFuturesNode([]byte(`{}`)); err != nil {
		t.Fatalf("空响应应零值非 err: %v", err)
	}
}
