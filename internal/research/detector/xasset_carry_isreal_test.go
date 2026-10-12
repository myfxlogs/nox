// XASSET_CARRY 虚实同层配对测试（D-319 T1-T3）：混层拒绝/同层放行/fail-open。
// 账户虚实实证（D-319 §1）：277842155:mt5=demo、3001157:mt5=real、gate:perp=demo(simulated)、
// binance:perp=real(simulated=false)。
package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/data/bus"
)

func boolPtr(b bool) *bool { return &b }

// isrealEquiv GOLD 四账户：real CEX（binance:perp）/demo CEX（gate:perp）/real MT（3001157）/demo MT（277842155）。
var isrealEquiv = fakeEquiv{byClass: map[string][]AssetEquivEntry{
	"GOLD": {
		{AssetClass: "GOLD", Canonical: "PAXGUSDT", VenueType: "perp", Venue: "binance"},
		{AssetClass: "GOLD", Canonical: "PAXGUSDT", VenueType: "perp", Venue: "gate"},
		{AssetClass: "GOLD", Canonical: "XAUUSD", VenueType: "mt5", Venue: "3001157"},
		{AssetClass: "GOLD", Canonical: "XAUUSD", VenueType: "mt5", Venue: "277842155"},
	},
}}

func isrealSnap(now time.Time) map[string]map[string]bus.Quote {
	return map[string]map[string]bus.Quote{
		"PAXGUSDT": {
			"binance:perp": {Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 4000.0, Ask: 4000.5, Time: now},
			"gate:perp":    {Symbol: "PAXGUSDT", Venue: "gate", VenueType: "perp", Bid: 4000.0, Ask: 4000.5, Time: now},
		},
		"XAUUSD": {
			"3001157:mt5":   {Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", Bid: 3999.8, Ask: 4000.0, Time: now},
			"277842155:mt5": {Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5", Bid: 3999.8, Ask: 4000.0, Time: now},
		},
	}
}

// isrealDetector 基准门槛 + 四账户等价类（newXAssetDetector 挂 goldEquiv 只含两账户）。
func isrealDetector() *XAssetCarryDetector {
	d := newXAssetDetector()
	d.AssetEquiv = isrealEquiv
	return d
}

func isrealFP(realMap map[string]*bool) fakeFP {
	return fakeFP{
		funding: map[string]map[string]float64{"PAXGUSDT": {"binance": 8.0, "gate": 6.0}},
		isReal:  realMap,
	}
}

// mixedCount 统计候选中「买腿与卖腿虚实不同」的条数。
func mixedCount(cands []Candidate, isReal map[string]*bool) int {
	n := 0
	for _, c := range cands {
		buyReal, okB := isReal[c.VenueBuy]
		sellReal, okS := isReal[c.VenueSell]
		_ = buyReal
		_ = sellReal
		if okB && okS && isReal[c.VenueBuy] != nil && isReal[c.VenueSell] != nil && *isReal[c.VenueBuy] != *isReal[c.VenueSell] {
			n++
		}
	}
	return n
}

func hasPair(cands []Candidate, a, b string) bool {
	for _, c := range cands {
		if (c.VenueBuy == a && c.VenueSell == b) || (c.VenueBuy == b && c.VenueSell == a) {
			return true
		}
	}
	return false
}

// TestXAssetIsReal_MixedRejected T1：demo MT（277842155，is_real=false）× real CEX
// （binance:perp，is_real=true）、real MT（3001157）× demo CEX（gate，is_real=false）
// → 混层组合零候选。
func TestXAssetIsReal_MixedRejected(t *testing.T) {
	realMap := map[string]*bool{
		"277842155:mt5": boolPtr(false),
		"3001157:mt5":   boolPtr(true),
		"binance:perp":  boolPtr(true),
		"gate:perp":     boolPtr(false),
	}
	cands := isrealDetector().Scan(context.Background(), isrealSnap(time.Now()), isrealFP(realMap))
	if n := mixedCount(cands, realMap); n != 0 {
		t.Fatalf("T1: mixed-layer candidates want 0, got %d (%+v)", n, cands)
	}
	for _, c := range cands {
		mixA := (c.VenueBuy == "277842155:mt5" && c.VenueSell == "binance:perp") || (c.VenueBuy == "binance:perp" && c.VenueSell == "277842155:mt5")
		mixB := (c.VenueBuy == "3001157:mt5" && c.VenueSell == "gate:perp") || (c.VenueBuy == "gate:perp" && c.VenueSell == "3001157:mt5")
		if mixA || mixB {
			t.Fatalf("T1: known mixed pair produced candidate: %s→%s", c.VenueBuy, c.VenueSell)
		}
	}
}

// TestXAssetIsReal_SameLayerPass T2：demo×demo（277842155×gate）与 real×real（3001157×binance）
// 各产候选（其余门槛照过——同快照下三门槛全放行）。
func TestXAssetIsReal_SameLayerPass(t *testing.T) {
	realMap := map[string]*bool{
		"277842155:mt5": boolPtr(false),
		"3001157:mt5":   boolPtr(true),
		"binance:perp":  boolPtr(true),
		"gate:perp":     boolPtr(false),
	}
	cands := isrealDetector().Scan(context.Background(), isrealSnap(time.Now()), isrealFP(realMap))
	if !hasPair(cands, "277842155:mt5", "gate:perp") {
		t.Fatalf("T2: demo×demo pair want candidate, got %+v", cands)
	}
	if !hasPair(cands, "3001157:mt5", "binance:perp") {
		t.Fatalf("T2: real×real pair want candidate, got %+v", cands)
	}
}

// TestXAssetIsReal_FailOpen T3：realMap 缺 binance 键（未知）→ 277842155×binance 放行
// （检测层 fail-open 惯例，可执行性由 gate 层 fail-closed 兜底）。
func TestXAssetIsReal_FailOpen(t *testing.T) {
	realMap := map[string]*bool{"277842155:mt5": boolPtr(false)} // binance 未知
	cands := isrealDetector().Scan(context.Background(), isrealSnap(time.Now()), isrealFP(realMap))
	if !hasPair(cands, "277842155:mt5", "binance:perp") {
		t.Fatalf("T3: fail-open (unknown venue) want pair allowed, got %+v", cands)
	}
}
