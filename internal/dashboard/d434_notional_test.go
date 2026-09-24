// d434_notional_test.go —— D-434 T1-T4：marginLegNotionalUSD 分域折叠纯函数矩阵。
package dashboard

import (
	"testing"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// TestD434_NotionalUSD T1-T4：分域折叠矩阵（T1=P1 主锚先红后绿）。
func TestD434_NotionalUSD(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	price := 4297.0
	cases := []struct {
		name string
		leg  pgstore.MarginOpenLeg
		want float64
	}{
		{"T1 CEX filled=基础币 cs=1", pgstore.MarginOpenLeg{
			OppID: "XASSET_CARRY:XAUUSD:a:b", Venue: "okx:perp", Symbol: "XAU-USDT-SWAP",
			Qty: 1016, FilledQty: f(1.016), Price: price,
		}, 1.016 * price}, // 红：旧码 1.016×0.001×price≈4.36
		{"T2 CEX filled缺 qty张×ct_val", pgstore.MarginOpenLeg{
			OppID: "XASSET_CARRY:XAUUSD:a:b", Venue: "okx:perp", Symbol: "XAU-USDT-SWAP",
			Qty: 1016, Price: price,
		}, 1016 * 0.001 * price},
		{"T3 MT filled=手数 cs不变", pgstore.MarginOpenLeg{
			OppID: "XASSET_CARRY:XAUUSD:a:b", Venue: "3001157:mt5", Symbol: "XAUUSDm",
			Qty: 0.01, FilledQty: f(0.01), Price: price,
		}, 0.01 * 100 * price},
		{"T4 spot qty=base cs=1", pgstore.MarginOpenLeg{
			OppID: "XASSET_CARRY:TRXUSDT:a:b", Venue: "binance:spot", Symbol: "TRXUSDT",
			Qty: 2, Price: 0.35,
		}, 2 * 0.35},
	}
	for _, c := range cases {
		got, ok := marginLegNotionalUSD(bus.New(), d434CS, c.leg)
		if !ok {
			t.Fatalf("%s: ok=false", c.name)
		}
		if got < c.want*0.99 || got > c.want*1.01 {
			t.Fatalf("%s: notional=%.2f want≈%.2f", c.name, got, c.want)
		}
	}
	// T1 量级对抗：CEX filled 路径不得低于 qty×ct_val 口径的 100 倍（防 1000x 低估回归）
	got, _ := marginLegNotionalUSD(bus.New(), d434CS, pgstore.MarginOpenLeg{
		OppID: "XASSET_CARRY:XAUUSD:a:b", Venue: "okx:perp", Symbol: "XAU-USDT-SWAP",
		Qty: 1016, FilledQty: func() *float64 { v := 1.016; return &v }(), Price: price,
	})
	if got < 100*price*0.001 {
		t.Fatalf("T1 量级断言 notional=%.2f（<100×ct_val 价=单位错位回归）", got)
	}
}
