package main

// D-542 A4：SKL 型毕业腿符号收编——graduation 腿+白名单 CEX canonical 并入
// watchlist 符号面与 TickSampler 采样集（listed 必订，unlisted 不造对名）。

import (
	"slices"
	"testing"

	"nox/internal/data/collect/exchange"
	"nox/internal/store/pgstore"
)

// fakeSymProvider 测试动态符号供给（venue→基币）。
type fakeSymProvider struct{ m map[string][]string }

func (f *fakeSymProvider) Symbols(venue string) []string { return f.m[venue] }

// 毕业行 SKLUSDT@binance spot_carry（venue_b 空=单边）→ 基币 SKL 入集。
func TestD542_GradLegBases(t *testing.T) {
	got := gradLegBases(
		[]pgstore.CarryGraduation{
			{Symbol: "SKLUSDT", VenueA: "binance", Status: "pending"},
			{Symbol: "XAUUSD", VenueA: "3001157", VenueB: "okx:perp", Status: "approved"}, // MT canonical 无 USDT 尾=不入
			{Symbol: "DEADUSDT", VenueA: "gate", Status: "rejected"},                      // rejected 腿不收
		},
		[]pgstore.XAssetAllowRow{
			{CanonicalBuy: "CL_USDT", VenueBuy: "gate", CanonicalSell: "CLUSDT", VenueSell: "binance"},
			{CanonicalBuy: "XAUUSD", VenueBuy: "3001157:mt5", CanonicalSell: "BTC-USDT-SWAP", VenueSell: "okx"},
		},
	)
	for _, want := range []string{"SKL", "CL", "BTC"} {
		if !got[want] {
			t.Fatalf("missing %s in %v", want, got)
		}
	}
	for _, bad := range []string{"XAU", "DEAD", "XAUUSD"} {
		if got[bad] {
			t.Fatalf("unexpected %s in %v", bad, got)
		}
	}
}

// tickPairSet：env ∪ provider 符号按三所形制展开——SKL 对 binance=SKLUSDT、okx=SKL-USDT-SWAP。
func TestD542_TickPairSet(t *testing.T) {
	prov := &fakeSymProvider{m: map[string][]string{"binance": {"SKL"}, "okx": {"SKL"}, "gate": {}}}
	pairs := tickPairSet(exchange.Config{Symbols: []string{"BTC"}, SymProvider: prov})
	for _, want := range []string{"SKLUSDT", "SKL-USDT-SWAP", "BTCUSDT", "BTC_USDT", "BTC-USDT-SWAP"} {
		if !slices.Contains(pairs, want) {
			t.Fatalf("missing %s in %v", want, pairs)
		}
	}
}
