// D-180 S2 单测：swapRateFor helper——实测优先/回落/7d 过期/中位数/换算/无报价 fail-closed/none。
// 用真实 DB（testAccumStore 模式），facts 表写入受控样本验证三态优先级。
package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
)

// TestD180_SwapRateFor_SettledHit 实测命中——settled 7d 内有样本 → 换算 bps 返回。
func TestD180_SwapRateFor_SettledHit(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapSettledShort, Venue: "61585565", Symbol: "GBPJPY", Value: -14.0, Unit: fact.UnitUsdLotDay, Ts: now.Add(-1 * time.Hour)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	qbus := bus.New()
	qbus.Publish(bus.Quote{Symbol: "GBPJPY", Venue: "61585565", VenueType: "mt5", Bid: 190.0, Ask: 190.02, VenueSymbol: "GBPJPY", Time: now})
	e := &Engine{store: s, quotes: qbus, cfg: Config{}}
	bps, src := e.swapRateFor(ctx, "61585565", "GBPJPY", "SELL")
	if src != swapSrcSettled {
		t.Fatalf("source: want settled, got %s", src)
	}
	wantBps := -14.0 / 190.01 * 10000
	if absDiffBps(bps, wantBps) > 0.01 {
		t.Errorf("bps: want %.4f, got %.4f", wantBps, bps)
	}
}

// TestD180_SwapRateFor_SettledExpiredFallbackSpec 实测过期（>7d）→ 回落 spec。
func TestD180_SwapRateFor_SettledExpiredFallbackSpec(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapSettledShort, Venue: "vA", Symbol: "GBPJPY", Value: -14.0, Unit: fact.UnitUsdLotDay, Ts: now.Add(-10 * 24 * time.Hour)},
		{Kind: fact.KindSwapShort, Venue: "vA", Symbol: "GBPJPY", Value: 0.5, Unit: fact.UnitBpsDaily, Ts: now.Add(-1 * time.Hour)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	e := &Engine{store: s, quotes: bus.New(), cfg: Config{}}
	bps, src := e.swapRateFor(ctx, "vA", "GBPJPY", "SELL")
	if src != swapSrcSpec {
		t.Fatalf("source: want spec (settled expired), got %s", src)
	}
	if bps != 0.5 {
		t.Errorf("bps: want 0.5 (spec), got %.4f", bps)
	}
}

// TestD180_SwapRateFor_NoSettledFallbackSpec 无实测 → spec 兜底。
func TestD180_SwapRateFor_NoSettledFallbackSpec(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 0.3, Unit: fact.UnitBpsDaily, Ts: now.Add(-1 * time.Hour)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	e := &Engine{store: s, quotes: bus.New(), cfg: Config{}}
	bps, src := e.swapRateFor(ctx, "vA", "AUDJPY", "BUY")
	if src != swapSrcSpec {
		t.Fatalf("source: want spec, got %s", src)
	}
	if bps != 0.3 {
		t.Errorf("bps: want 0.3, got %.4f", bps)
	}
}

// TestD180_SwapRateFor_NoneFailClosed 无 settled 无 spec → (0, "none") fail-closed。
func TestD180_SwapRateFor_NoneFailClosed(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	e := &Engine{store: s, quotes: bus.New(), cfg: Config{}}
	bps, src := e.swapRateFor(ctx, "vA", "AUDJPY", "BUY")
	if src != swapSrcNone {
		t.Fatalf("source: want none, got %s", src)
	}
	if bps != 0 {
		t.Errorf("bps: want 0 (none), got %.4f", bps)
	}
}

// TestD180_SwapRateFor_SettledNoQuoteFallbackSpec 实测命中但无报价 → 回落 spec。
func TestD180_SwapRateFor_SettledNoQuoteFallbackSpec(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapSettledShort, Venue: "vA", Symbol: "GBPJPY", Value: -14.0, Unit: fact.UnitUsdLotDay, Ts: now.Add(-1 * time.Hour)},
		{Kind: fact.KindSwapShort, Venue: "vA", Symbol: "GBPJPY", Value: 0.5, Unit: fact.UnitBpsDaily, Ts: now.Add(-1 * time.Hour)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	e := &Engine{store: s, quotes: bus.New(), cfg: Config{}} // 无报价
	bps, src := e.swapRateFor(ctx, "vA", "GBPJPY", "SELL")
	if src != swapSrcSpec {
		t.Fatalf("source: want spec (no quote fallback), got %s", src)
	}
	if bps != 0.5 {
		t.Errorf("bps: want 0.5 (spec fallback), got %.4f", bps)
	}
}

// TestD180_SwapRateFor_MedianMultipleSettled 多样本中位数。
func TestD180_SwapRateFor_MedianMultipleSettled(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapSettledShort, Venue: "vA", Symbol: "GBPJPY", Value: -10.0, Unit: fact.UnitUsdLotDay, Ts: now.Add(-1 * time.Hour)},
		{Kind: fact.KindSwapSettledShort, Venue: "vA", Symbol: "GBPJPY", Value: -14.0, Unit: fact.UnitUsdLotDay, Ts: now.Add(-2 * time.Hour)},
		{Kind: fact.KindSwapSettledShort, Venue: "vA", Symbol: "GBPJPY", Value: -20.0, Unit: fact.UnitUsdLotDay, Ts: now.Add(-3 * time.Hour)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	qbus := bus.New()
	qbus.Publish(bus.Quote{Symbol: "GBPJPY", Venue: "vA", VenueType: "mt5", Bid: 190.0, Ask: 190.02, VenueSymbol: "GBPJPY", Time: now})
	e := &Engine{store: s, quotes: qbus, cfg: Config{}}
	bps, src := e.swapRateFor(ctx, "vA", "GBPJPY", "SELL")
	if src != swapSrcSettled {
		t.Fatalf("source: want settled, got %s", src)
	}
	wantBps := -14.0 / 190.01 * 10000 // 中位数 -14.0
	if absDiffBps(bps, wantBps) > 0.01 {
		t.Errorf("bps: want %.4f (median -14.0), got %.4f", wantBps, bps)
	}
}

// TestD180_SwapSrcDiscount spec 源折扣（仅入场门禁用）。
func TestD180_SwapSrcDiscount(t *testing.T) {
	if got := swapSrcDiscount(2.0, swapSrcSpec, 0.5); absDiffBps(got, 1.0) > 1e-9 {
		t.Errorf("spec×0.5: want 1.0, got %.4f", got)
	}
	if got := swapSrcDiscount(2.0, swapSrcSettled, 0.5); absDiffBps(got, 2.0) > 1e-9 {
		t.Errorf("settled no discount: want 2.0, got %.4f", got)
	}
	if got := swapSrcDiscount(2.0, swapSrcNone, 0.5); absDiffBps(got, 2.0) > 1e-9 {
		t.Errorf("none no discount: want 2.0, got %.4f", got)
	}
	if got := swapSrcDiscount(2.0, swapSrcSpec, 0); absDiffBps(got, 2.0) > 1e-9 {
		t.Errorf("spec discount=0: want 2.0, got %.4f", got)
	}
}

// TestD180_SwapRateForNet 净 swap + source 聚合（双腿 settled）。
func TestD180_SwapRateForNet(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapSettledLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitUsdLotDay, Ts: now.Add(-1 * time.Hour)},
		{Kind: fact.KindSwapSettledShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitUsdLotDay, Ts: now.Add(-1 * time.Hour)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	qbus := bus.New()
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5", Bid: 100.0, Ask: 100.02, VenueSymbol: "AUDJPY", Time: now})
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vB", VenueType: "mt5", Bid: 100.0, Ask: 100.02, VenueSymbol: "AUDJPY", Time: now})
	e := &Engine{store: s, quotes: qbus, cfg: Config{}}
	net, src := e.swapRateForNet(ctx, "vA", "vB", "AUDJPY")
	if src != swapSrcSettled {
		t.Fatalf("source: want settled, got %s", src)
	}
	wantLong := 5.0 / 100.01 * 10000
	wantShort := -3.0 / 100.01 * 10000
	wantNet := wantLong + wantShort
	if absDiffBps(net, wantNet) > 0.1 {
		t.Errorf("net: want %.4f, got %.4f", wantNet, net)
	}
}

// TestD180_SwapRateForNet_MixedSource 混合源（long settled + short spec）→ source=spec（保守）。
func TestD180_SwapRateForNet_MixedSource(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapSettledLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitUsdLotDay, Ts: now.Add(-1 * time.Hour)},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: 0.3, Unit: fact.UnitBpsDaily, Ts: now.Add(-1 * time.Hour)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	qbus := bus.New()
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5", Bid: 100.0, Ask: 100.02, VenueSymbol: "AUDJPY", Time: now})
	e := &Engine{store: s, quotes: qbus, cfg: Config{}}
	_, src := e.swapRateForNet(ctx, "vA", "vB", "AUDJPY")
	if src != swapSrcSpec {
		t.Fatalf("mixed source: want spec (conservative), got %s", src)
	}
}

// TestD180_SwapRateForNet_NoneFailClosed 任一 none → none fail-closed。
func TestD180_SwapRateForNet_NoneFailClosed(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapSettledLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitUsdLotDay, Ts: now.Add(-1 * time.Hour)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	qbus := bus.New()
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5", Bid: 100.0, Ask: 100.02, VenueSymbol: "AUDJPY", Time: now})
	e := &Engine{store: s, quotes: qbus, cfg: Config{}}
	_, src := e.swapRateForNet(ctx, "vA", "vB", "AUDJPY")
	if src != swapSrcNone {
		t.Fatalf("one none: want none (fail-closed), got %s", src)
	}
}

// TestD180_MedianFloat64 中位数函数。
func TestD180_MedianFloat64(t *testing.T) {
	if m := medianFloat64([]float64{5.0}); m != 5.0 {
		t.Errorf("single: want 5.0, got %.4f", m)
	}
	if m := medianFloat64([]float64{1.0, 3.0, 5.0}); m != 3.0 {
		t.Errorf("odd: want 3.0, got %.4f", m)
	}
	if m := medianFloat64([]float64{1.0, 2.0, 3.0, 4.0}); m != 2.5 {
		t.Errorf("even: want 2.5, got %.4f", m)
	}
	if m := medianFloat64([]float64{}); m != 0 {
		t.Errorf("empty: want 0, got %.4f", m)
	}
}

// TestD180_SwapSrcFromVenueKey bare venue 提取。
func TestD180_SwapSrcFromVenueKey(t *testing.T) {
	if got := swapSrcFromVenueKey("61585565:mt5"); got != "61585565" {
		t.Errorf("mt5 suffix: want 61585565, got %s", got)
	}
	if got := swapSrcFromVenueKey("vA"); got != "vA" {
		t.Errorf("no suffix: want vA, got %s", got)
	}
}

// absDiffBps 浮点比较容差。
func absDiffBps(a, b float64) float64 {
	if a < b {
		return b - a
	}
	return a - b
}
