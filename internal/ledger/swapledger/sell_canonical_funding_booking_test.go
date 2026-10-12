package swapledger

// 断层④ funding 记账侧向 canonical 测试锚 T8。
// SSOT=docs/D-329+D-330设计与施工提示词-腿名义匹配与黄金demo放行-2026-09-18-Devin.md §2.3/§3。
//
// 黄金对：BUY 277842155:mt5 XAUUSDm（swap=0 免息）+ SELL binance:perp XAUUSDT（funding 主网真值）。
// 现码双腿共用买侧 canonical XAUUSD 查 fact → fact_not_found{XAUUSD,binance} → funding 永不入账（红）；
// 修后 SELL 腿走侧向 canonical 三段链（opp.canonical_sell→broker_symbol_map→canonicalFromOppID）。

import (
	"context"
	"testing"
	"time"

	"arb/internal/data/fact"
	"arb/internal/store/pgstore"
)

func d330strptr(s string) *string { return &s }

// d330GoldenLegs 黄金对双腿：SELL perp（kind=funding）+ BUY mt5（kind=swap，rate=0 → zero_swap skip）。
func d330GoldenLegs() []pgstore.PositionLeg {
	return []pgstore.PositionLeg{
		{LegNo: 0, Phase: "OPEN", Venue: "binance:perp", Symbol: "XAUUSDT", Side: "SELL", Qty: 0.01, Status: "FILLED"},
		{LegNo: 1, Phase: "OPEN", Venue: "277842155:mt5", Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01, Status: "FILLED"},
	}
}

// d330GoldenMaps 黄金对 fact 表：funding 按 SELL 侧 canonical 存（XAUUSDT@binance），
// swap_long=0（Exness 免息，D-300 override 生产形态）。
func d330GoldenMaps() (map[factKey]fact.Fact, map[factKey]fact.Fact, map[factKey]fact.Fact) {
	now := time.Now()
	longMap := map[factKey]fact.Fact{factKey{"XAUUSD", "277842155"}: {Value: 0, Ts: now}}
	fundMap := map[factKey]fact.Fact{factKey{"XAUUSDT", "binance"}: {Value: 22.9, Ts: now}} // 主网真值 %/yr
	return longMap, map[factKey]fact.Fact{}, fundMap
}

func d330Stubs(store *nonSettleStubStore) *Ledger {
	notional := stubNotional{vals: map[string]float64{
		"XAUUSDT:binance:perp:bid": 435500, // SELL 腿（侧向 canonical 命中后可达；现码用 XAUUSD 查不到）
		"XAUUSD:277842155:mt5:ask": 435500, // BUY 腿
	}}
	r := stubTripleDay{venueDays: map[string]time.Weekday{"277842155": time.Wednesday}}
	return &Ledger{store: store, notional: notional, tripleDay: r}
}

// TestD330_SellCanonical_FundingBooking T8 主锚：opp.canonical_sell=XAUUSDT → SELL 腿按
// {XAUUSDT,binance} 命中 funding fact 入账（红=现码 fact_not_found{XAUUSD,binance} 整腿 skip）。
func TestD330_SellCanonical_FundingBooking(t *testing.T) {
	store := &nonSettleStubStore{
		hasCashflowLegs: map[int16]bool{},
		legs:            d330GoldenLegs(),
		opp: &pgstore.Opportunity{
			ID: "XASSET_CARRY:XAUUSD:277842155:mt5:binance:perp", Canonical: "XAUUSD",
			CanonicalSell: d330strptr("XAUUSDT"),
		},
	}
	l := d330Stubs(store)
	longMap, shortMap, fundMap := d330GoldenMaps()
	pos := pgstore.Position{ID: 486, Type: "XASSET_CARRY", OpportunityID: "XASSET_CARRY:XAUUSD:277842155:mt5:binance:perp"}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	n, err := l.accountPosition(context.Background(), pos, monday, longMap, shortMap, fundMap)
	if err != nil {
		t.Fatalf("accountPosition: %v", err)
	}
	// BUY 腿 swap=0 → zero_swap skip（免息前提语义正确）；SELL 腿 funding 入账（红=现码 0 行）
	if n != 1 || len(store.insertedCFs) != 1 {
		t.Fatalf("written=%d inserted=%d, want 1（SELL 腿 funding 入账；红=现码 fact_not_found 双腿全 skip）", n, len(store.insertedCFs))
	}
	cf := store.insertedCFs[0]
	if cf.Kind != "funding" {
		t.Errorf("kind=%q, want funding（perp SELL 腿）", cf.Kind)
	}
	if cf.LegNo == nil || *cf.LegNo != 0 {
		t.Errorf("legNo=%v, want 0（binance:perp 腿）", cf.LegNo)
	}
	// 金额=22.9%/yr ÷365 × notional(0.01×435500=4355) ≈ $2.7323（正=空头收）
	want := 22.9 / 100 / 365 * 4355
	if cf.AmountUSD <= 0 || absF(cf.AmountUSD-want) > 1e-3 {
		t.Errorf("amount=%v, want ≈%v（正号=空头收 funding）", cf.AmountUSD, want)
	}
}

// TestD330_SellCanonical_MapFallback T8 链②：无 opp 行（canonical_sell 不可得）→
// 卖腿 broker_symbol_map 兜底解析 XAUUSDT → funding 同样入账（镜像 D-311 xassetSellCanonical）。
func TestD330_SellCanonical_MapFallback(t *testing.T) {
	store := &nonSettleStubStore{
		hasCashflowLegs: map[int16]bool{},
		legs:            d330GoldenLegs(),
		maps: []pgstore.SymbolMapping{
			{Venue: "binance", BrokerSymbol: "XAUUSDT", Canonical: "XAUUSDT", Source: "manual"},
		},
	}
	l := d330Stubs(store)
	longMap, shortMap, fundMap := d330GoldenMaps()
	pos := pgstore.Position{ID: 487, Type: "XASSET_CARRY", OpportunityID: "XASSET_CARRY:XAUUSD:277842155:mt5:binance:perp"}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	n, err := l.accountPosition(context.Background(), pos, monday, longMap, shortMap, fundMap)
	if err != nil {
		t.Fatalf("accountPosition: %v", err)
	}
	if n != 1 || len(store.insertedCFs) != 1 || store.insertedCFs[0].Kind != "funding" {
		t.Fatalf("map 兜底应入账 funding, written=%d inserted=%v", n, store.insertedCFs)
	}
}

// TestD330_SellCanonical_FallbackIsBuyCanonical T8 对照：opp 无 canonical_sell 且无映射
// → 回落 canonicalFromOppID（=现状行为）：SELL 腿 fact_not_found skip、BUY 腿 zero_swap skip、零入账。
func TestD330_SellCanonical_FallbackIsBuyCanonical(t *testing.T) {
	store := &nonSettleStubStore{
		hasCashflowLegs: map[int16]bool{},
		legs:            d330GoldenLegs(),
	}
	l := d330Stubs(store)
	longMap, shortMap, fundMap := d330GoldenMaps()
	pos := pgstore.Position{ID: 488, Type: "XASSET_CARRY", OpportunityID: "XASSET_CARRY:XAUUSD:277842155:mt5:binance:perp"}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	n, err := l.accountPosition(context.Background(), pos, monday, longMap, shortMap, fundMap)
	if err != nil {
		t.Fatalf("accountPosition: %v", err)
	}
	if n != 0 || len(store.insertedCFs) != 0 {
		t.Fatalf("回落现状应零入账（SELL fact_not_found + BUY zero_swap）, written=%d inserted=%d", n, len(store.insertedCFs))
	}
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
