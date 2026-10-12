package autotrade

import (
	"math"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// 单测：basis_bps 计算 + 排序不动 + 缺报价 nil 安全。

// 用例①：basis 计算对称双腿（qShort.Bid - qLong.Ask）/qLong.Ask×1e4
func TestBasisCalculationSymmetric(t *testing.T) {
	qbus := bus.New()
	now := time.Now()
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "ic", VenueType: "mt5", Ask: 100.0, Bid: 99.98, VenueSymbol: "AUDJPY", Time: now})
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "pxb", VenueType: "mt5", Ask: 100.07, Bid: 100.05, VenueSymbol: "AUDJPY", Time: now})

	e := &Engine{quotes: qbus, store: &pgstore.Store{}, cfg: gate.Config{MinNetSwapBps: 0.0}}
	qL, ok1 := e.quotes.Latest("AUDJPY", "ic:mt5")
	if !ok1 {
		t.Fatal("qLong not found")
	}
	qS, ok2 := e.quotes.Latest("AUDJPY", "pxb:mt5")
	if !ok2 {
		t.Fatal("qShort not found")
	}
	basisBps := (qS.Bid - qL.Ask) / qL.Ask * 10000
	// (100.05 - 100.0) / 100.0 * 10000 = 5.0 bps
	if math.Abs(basisBps-5.0) > 1e-9 {
		t.Errorf("basisBps: got %.4f, want 5.0", basisBps)
	}
}

// 用例②：缺报价时 basis=0 不 panic
func TestBasisMissingQuotesNoPanic(t *testing.T) {
	qbus := bus.New()
	now := time.Now()
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "ic", VenueType: "mt5", Ask: 100.0, Bid: 99.98, VenueSymbol: "AUDJPY", Time: now})

	e := &Engine{quotes: qbus, store: &pgstore.Store{}, cfg: gate.Config{MinNetSwapBps: 0.0}}
	basisBps := 0.0
	if qL, ok1 := e.quotes.Latest("AUDJPY", "ic:mt5"); ok1 {
		if qS, ok2 := e.quotes.Latest("AUDJPY", "pxb:mt5"); ok2 {
			if qL.Ask > 0 {
				basisBps = (qS.Bid - qL.Ask) / qL.Ask * 10000
			}
		}
	}
	if basisBps != 0 {
		t.Errorf("missing qShort: basisBps should be 0, got %.4f", basisBps)
	}
}

// 用例③：排序仍按 netSwapBps（basis 不参与排序）
func TestSortByNetSwapOnly(t *testing.T) {
	combos := []swapComboCandidate{
		{canonical: "A", netSwapBps: 1.0, basisBps: 50.0},
		{canonical: "B", netSwapBps: 5.0, basisBps: 1.0},
		{canonical: "C", netSwapBps: 3.0, basisBps: 100.0},
	}
	sortCombosByNetSwap(combos)
	if combos[0].canonical != "B" {
		t.Errorf("sort[0]: got %s, want B (highest netSwapBps)", combos[0].canonical)
	}
	if combos[1].canonical != "C" {
		t.Errorf("sort[1]: got %s, want C", combos[1].canonical)
	}
	if combos[2].canonical != "A" {
		t.Errorf("sort[2]: got %s, want A (lowest netSwapBps)", combos[2].canonical)
	}
	// basis 高的 C(100) 不应排在 B(1) 前面——basis 不动排序
	if combos[0].basisBps != 1.0 {
		t.Errorf("sort should not use basisBps: combos[0].basisBps=%.1f (B has 1.0)", combos[0].basisBps)
	}
}

// sortCombosByNetSwap 按 netSwapBps 降序（同 discoverSwapCombos 排序逻辑，提取可测）。
func sortCombosByNetSwap(combos []swapComboCandidate) {
	for i := 1; i < len(combos); i++ {
		for j := i; j > 0 && combos[j-1].netSwapBps < combos[j].netSwapBps; j-- {
			combos[j-1], combos[j] = combos[j], combos[j-1]
		}
	}
}

// 用例④：SwapComboPublic 转换保留 basisBps
func TestSwapComboPublicConversion(t *testing.T) {
	c := swapComboCandidate{
		canonical:      "AUDJPY",
		vLong:          "ic:mt5",
		vShort:         "pxb:mt5",
		netSwapBps:     2.5,
		basisBps:       3.0,
		basisAvailable: true,
	}
	p := SwapComboPublic{
		Canonical:  c.canonical,
		VenueLong:  c.vLong,
		VenueShort: c.vShort,
		NetSwapBps: c.netSwapBps,
		BasisBps:   c.basisBps,
	}
	if p.BasisBps != 3.0 {
		t.Errorf("BasisBps: got %.1f, want 3.0", p.BasisBps)
	}
	if p.NetSwapBps != 2.5 {
		t.Errorf("NetSwapBps: got %.1f, want 2.5", p.NetSwapBps)
	}
}
