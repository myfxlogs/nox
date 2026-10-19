// gate_test.go D-462 pair 键与消费端口径锚定（T2）——fetchGateTicker REST 四例
// 随 D-591 S3 gate 腿读库化退役（替代=gold_read_test.go 三态 DB 测）。
package main

import (
	"strings"
	"testing"
)

// T2：gate pair 键与消费端口径钉死——engine_opp.go pairKey=CanonicalSell+"-"+TrimSuffix(Canonical,"USD")。
// canonical=MT 侧品种（XAUUSD/XPTUSD/XPDUSD），CanonicalSell=CEX 侧 canonical（XAUUSDT 等去 _）。
func TestGatePairKeys(t *testing.T) {
	want := map[string][2]string{ // sym → {canonicalSell, canonical} → pair
		"XAU_USDT": {"XAUUSDT", "XAUUSD"},
		"XPT_USDT": {"XPTUSDT", "XPTUSD"},
		"XPD_USDT": {"XPDUSDT", "XPDUSD"},
	}
	seen := map[string]string{}
	// 清单改白名单派生后，本测改用生产形 fixture 派生（gate 三行语义不变）。
	pairs, _ := deriveMonitorPairs(d472Combos(), d472Universe(), nil, nil, nil, nil)
	for _, p := range pairs {
		if p.cexVenue == "gate:perp" {
			seen[p.sym] = p.pair
		}
	}
	if len(seen) != 3 {
		t.Fatalf("gate pairs=%v", seen)
	}
	for sym, cs := range want {
		exp := cs[0] + "-" + strings.TrimSuffix(cs[1], "USD")
		if seen[sym] != exp {
			t.Errorf("pair[%s]=%s want %s", sym, seen[sym], exp)
		}
	}
}
