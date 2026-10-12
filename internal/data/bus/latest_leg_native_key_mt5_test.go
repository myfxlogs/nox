// d571c2_leg_test.go —— D-571 包C 补充 S3：LatestLeg MT 分支原生键优先+canonical 兜底。
package bus

import (
	"testing"
	"time"
)

func TestD571C2_LatestLegNativeKeyMT5(t *testing.T) {
	b := New()
	vk := "3001157:mt5"
	// blocked 变体按原生键发布（emitBlocked 降格形态）→ canonical 直查永 miss，原生键命中。
	b.Publish(Quote{Symbol: "XAUUSD247m", Venue: "3001157", VenueType: "mt5", Bid: 3000, Ask: 3000.4, Time: time.Now()})
	q, ok := b.LatestLeg("XAUUSD", "XAUUSD247m", vk)
	if !ok || q.Bid != 3000 {
		t.Fatalf("原生键应命中 bid=3000, ok=%v q=%+v", ok, q)
	}
	// primary 变体按 canonical 发布 → 原生 miss → canonical 兜底仍活。
	b2 := New()
	b2.Publish(Quote{Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", Bid: 2000, Ask: 2000.2, Time: time.Now()})
	q2, ok2 := b2.LatestLeg("XAUUSD", "XAUUSDm", vk)
	if !ok2 || q2.Bid != 2000 {
		t.Fatalf("canonical 兜底应命中 bid=2000, ok=%v q=%+v", ok2, q2)
	}
	// 反向污染阴性：canonical 价不得命中 blocked 变体腿的原生键查询优先位。
	if _, ok3 := b.LatestLeg("XAUUSD", "XAUUSD247m", vk); !ok3 {
		t.Fatal("原生键在位应命中")
	}
}
