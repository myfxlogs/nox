// d374_class_filter_test.go —— D-374 A 面：divergence 资产类别过滤锚（先红后绿）。
// 生产实证：FUNDING_DIVERGENCE AVGO binance:okx +30.5% vs +0.0%——股票 perp 休市冻结费率
// vs live 数=结构性伪发散（TSLA/QCOM 类同隐患）。压 digest 不删：classFiltered 计数返回
// 调用方（digest 汇总行渲染，与 leg=无 并列模式）。
package main

import "testing"

// mkClass 构造带资产类的 normRow（Class=fin_cex_universe.underlying_class 既有字段）。
func mkClass(venue, symbol, class string, ann float64) normRow {
	return normRow{Row: Row{Symbol: symbol, Class: class, Vol24h: 6e6}, Venue: venue,
		Canonical: canonicalOf(normBase(symbol)), FundingAnn: fptr(ann)}
}

// TestDivergences_ClassFiltered D-374 A 面三锚：
// ①EQUITY 双侧 diverge → 零事件+classFiltered 计入；
// ②COIN 双侧 diverge → 正常产（豁免集流通：COIN/COMMODITY/FX/空）；
// ③一侧 EQUITY 一侧 NULL → 压制（任一侧非空命中即滤，双侧矛盾取非空侧=宁可信其有）。
func TestDivergences_ClassFiltered(t *testing.T) {
	rows := []normRow{
		mkClass("binance", "AVGOUSDT", "EQUITY", 30.5),
		mkClass("okx", "AVGO-USDT-SWAP", "EQUITY", 0.0), // |30.5−0|=30.5≥20，但 EQUITY → 压
		mkClass("binance", "BTCUSDT", "COIN", 25.0),
		mkClass("okx", "BTC-USDT-SWAP", "COIN", 2.0), // |25−2|=23≥20 → 正常产
		mkClass("binance", "TSLAUSDT", "EQUITY", 18.0),
		mkClass("okx", "TSLA-USDT-SWAP", "", 1.0), // 一侧非空 EQUITY → 压
	}
	pairs, filtered := divergences(rows, 20, 5e6) // D-376：签名改 divPair 返回
	if len(pairs) != 1 {
		t.Fatalf("锚A: want 1 pair（BTC only）, got %d: %v", len(pairs), pairs)
	}
	if pairs[0].Canon != "BTC" {
		t.Fatalf("锚A: want BTC pair, got %+v", pairs[0])
	}
	if len(filtered) != 2 || filtered[0] != "AVGO" || filtered[1] != "TSLA" {
		t.Fatalf("锚A: classFiltered want [AVGO TSLA], got %v", filtered)
	}
}
