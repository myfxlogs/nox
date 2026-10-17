// factcache_test.go —— N3 判龄/并发锚（退回㉔锚②③）。
package engine

import (
	"testing"
	"time"

	"nox/internal/data/fact"
)

// TestFactCache_LazyAge D-604 N3 锚①：SetAgeGates 后陈键被滤/新鲜键过。
func TestFactCache_LazyAge(t *testing.T) {
	fc := NewFactCache()
	fc.SetAgeGates(30*time.Minute, 30*time.Minute)
	stale := time.Now().Add(-2 * time.Hour)
	fresh := time.Now().Add(-1 * time.Minute)
	fc.UpdateFacts([]fact.Fact{
		{Kind: fact.KindFunding, Symbol: "STALE", Venue: "gate", Value: 0.01, Ts: stale},
		{Kind: fact.KindFunding, Symbol: "FRESH", Venue: "gate", Value: 0.02, Ts: fresh},
		{Kind: fact.KindSwapLong, Symbol: "STALES", Venue: "gate", Value: 1, Ts: stale},
		{Kind: fact.KindSwapLong, Symbol: "FRESHS", Venue: "gate", Value: 2, Ts: fresh},
	})
	got := fc.FundingFacts()
	if _, ok := got["STALE"]; ok {
		t.Fatal("陈 funding 键未滤")
	}
	if v, ok := got["FRESH"]["gate"]; !ok || v != 0.02 {
		t.Fatalf("鲜 funding 键未过：%v %v", v, ok)
	}
	sw := fc.SwapFacts()
	if _, ok := sw["STALES"]; ok {
		t.Fatal("陈 swap 键未滤")
	}
	if v, ok := sw["FRESHS"]["gate"]; !ok || v[0] != 2 {
		t.Fatalf("鲜 swap 键未过：%v %v", v, ok)
	}
}

// TestFactCache_LazyAge_Concurrent 退回㉔锚②：UpdateFacts ∥ FundingFacts/SwapFacts
// 并发——-race 下慢径必走（minAt 陈旧触发逐键滤），at 快照原子化钉死数据竞争。
func TestFactCache_LazyAge_Concurrent(t *testing.T) {
	fc := NewFactCache()
	fc.SetAgeGates(1, 1) // 1ns 窗——写入即陈旧=慢径必走
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			fc.UpdateFacts([]fact.Fact{
				{Kind: fact.KindFunding, Symbol: "S", Venue: "v", Value: float64(i), Ts: time.Now()},
				{Kind: fact.KindSwapLong, Symbol: "S", Venue: "v", Value: float64(i), Ts: time.Now()},
			})
		}
	}()
	for i := 0; i < 200; i++ {
		_ = fc.FundingFacts()
		_ = fc.SwapFacts()
	}
	<-done
}

// TestFactCache_LazyAge_DefaultFallback 退回㉔锚③：SetAgeGates 缺省 0=闸禁用
// 保旧行为（零值不滤——装配层 fallback 已注 24h；本层零值=禁用语义）。
func TestFactCache_LazyAge_DefaultFallback(t *testing.T) {
	fc := NewFactCache()
	fc.SetAgeGates(0, 0) // 零值=禁用
	fc.UpdateFacts([]fact.Fact{
		{Kind: fact.KindFunding, Symbol: "S", Venue: "v", Value: 1, Ts: time.Now().Add(-48 * time.Hour)},
	})
	if _, ok := fc.FundingFacts()["S"]; !ok {
		t.Fatal("零窗禁用时陈键被误滤")
	}
}
