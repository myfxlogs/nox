// xasset_carry_freshtier_test.go —— STATE 行44 锚：交割腿鲜度分档——futures
// 稀疏节拍（>120s 天然）过门、真断流（>45min）仍拒；perp/spot 阈零动。
package detector

import (
	"testing"
	"time"

	"nox/internal/data/bus"
)

func TestLegFreshTTL_Tiers(t *testing.T) {
	base := 120 * time.Second
	if legFreshTTL(base, "futures") != delivFreshTTL {
		t.Fatal("futures 应走宽档")
	}
	for _, vt := range []string{"spot", "perp", "mt5", ""} {
		if legFreshTTL(base, vt) != base {
			t.Fatalf("%s 应保主阈: %v", vt, legFreshTTL(base, vt))
		}
	}
	// 稀疏节拍（16-35min 实测峰值内）过门；真断流仍拒（45min 界语义）。
	now := time.Now()
	if stale(bus.Quote{Time: now.Add(-30 * time.Minute)}, now, delivFreshTTL) {
		t.Fatal("30min 帧距应过门（天然稀疏）")
	}
	if !stale(bus.Quote{Time: now.Add(-time.Hour)}, now, delivFreshTTL) {
		t.Fatal("1h 真断流应仍拒")
	}
}
