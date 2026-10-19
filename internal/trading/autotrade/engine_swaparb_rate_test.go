// 单测：swapRateFor helper——实测优先/回落/7d 过期/中位数/换算/无报价 fail-closed/none。
// 用真实 DB（testAccumStore 模式），facts 表写入受控样本验证三态优先级。
package autotrade

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
	"nox/internal/risk/gate"
)

// TestD180_SwapRateFor_SettledHit 实测命中——settled 7d 内有样本 → 换算 bps 返回。
// JPY 对需 USDJPY 换算（notionalUSD = CS × mid ÷ USDJPY）。
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
	qbus.Publish(bus.Quote{Symbol: "USDJPY", Venue: "fx", VenueType: "mt5", Bid: 150.0, Ask: 150.01, VenueSymbol: "USDJPY", Time: now})
	e := &Engine{store: s, quotes: qbus, cfg: gate.Config{}}
	bps, src := e.swapRateFor(ctx, "61585565", "GBPJPY", "SELL", 0)
	if src != swapSrcSettled {
		t.Fatalf("source: want settled, got %s", src)
	}
	// CS=1（无 resolver），notionalUSD = 1 × 190.01 ÷ 150.005 = 1.2667
	// bps = -14 / 1.2667 × 10000 = -110526（CS=1 测试设定，真实 CS=100000 时 bps=-1.105）
	wantNotionalUSD := 1.0 * 190.01 / 150.005
	wantBps := -14.0 / wantNotionalUSD * 10000
	if absDiffBps(bps, wantBps) > 0.1 {
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
	e := &Engine{store: s, quotes: bus.New(), cfg: gate.Config{}}
	bps, src := e.swapRateFor(ctx, "vA", "GBPJPY", "SELL", 0)
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
	e := &Engine{store: s, quotes: bus.New(), cfg: gate.Config{}}
	bps, src := e.swapRateFor(ctx, "vA", "AUDJPY", "BUY", 0)
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
	e := &Engine{store: s, quotes: bus.New(), cfg: gate.Config{}}
	bps, src := e.swapRateFor(ctx, "vA", "AUDJPY", "BUY", 0)
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
	e := &Engine{store: s, quotes: bus.New(), cfg: gate.Config{}} // 无报价
	bps, src := e.swapRateFor(ctx, "vA", "GBPJPY", "SELL", 0)
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
	qbus.Publish(bus.Quote{Symbol: "USDJPY", Venue: "fx", VenueType: "mt5", Bid: 150.0, Ask: 150.01, VenueSymbol: "USDJPY", Time: now})
	e := &Engine{store: s, quotes: qbus, cfg: gate.Config{}}
	bps, src := e.swapRateFor(ctx, "vA", "GBPJPY", "SELL", 0)
	if src != swapSrcSettled {
		t.Fatalf("source: want settled, got %s", src)
	}
	// JPY 换算 notionalUSD = 1 × 190.01 ÷ 150.005
	wantNotionalUSD := 1.0 * 190.01 / 150.005
	wantBps := -14.0 / wantNotionalUSD * 10000 // 中位数 -14.0
	if absDiffBps(bps, wantBps) > 0.1 {
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
	qbus.Publish(bus.Quote{Symbol: "USDJPY", Venue: "fx", VenueType: "mt5", Bid: 150.0, Ask: 150.01, VenueSymbol: "USDJPY", Time: now})
	e := &Engine{store: s, quotes: qbus, cfg: gate.Config{}}
	net, src := e.swapRateForNet(ctx, "vA", "vB", "AUDJPY")
	if src != swapSrcSettled {
		t.Fatalf("source: want settled, got %s", src)
	}
	// JPY 换算 notionalUSD = 1 × 100.01 ÷ 150.005
	wantNotionalUSD := 1.0 * 100.01 / 150.005
	wantLong := 5.0 / wantNotionalUSD * 10000
	wantShort := -3.0 / wantNotionalUSD * 10000
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
	qbus.Publish(bus.Quote{Symbol: "USDJPY", Venue: "fx", VenueType: "mt5", Bid: 150.0, Ask: 150.01, VenueSymbol: "USDJPY", Time: now})
	e := &Engine{store: s, quotes: qbus, cfg: gate.Config{}}
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
	qbus.Publish(bus.Quote{Symbol: "USDJPY", Venue: "fx", VenueType: "mt5", Bid: 150.0, Ask: 150.01, VenueSymbol: "USDJPY", Time: now})
	e := &Engine{store: s, quotes: qbus, cfg: gate.Config{}}
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

// TestSwapRateFor_KeyDomainResolved 锚①（键域错位第 7 例）：组合行 canonical=裸形
// TRX（1055 后），facts 写侧键=venue 原生符号（TRXUSD）——种 broker_symbol_map
// 反解行+原生键 spec facts → swapRateFor 经候选集命中 spec 不再 none（生产实锤
// 「swapRateFor none venue=15032313 canonical=TRX」根修）。
// 对抗锚：①canonical 标签行兜底（D-325：无 map 行 venue 的 facts 键=canonical
// 本形→仍命中）；②真无 facts→none fail-closed 语义保留。
func TestSwapRateFor_KeyDomainResolved(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `DELETE FROM broker_symbol_map WHERE venue LIKE '999905%'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(ctx, `DELETE FROM broker_symbol_map WHERE venue LIKE '999905%'`)
		_, _ = s.Pool().Exec(ctx, `DELETE FROM broker_accounts WHERE venue='99990513'`)
	})
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, host, port, password, venue_type)
		 VALUES ('99990513','swaprate-test.invalid',443,'x','mt5')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, product_group, is_primary, source)
		 VALUES ('99990513','TRXUSD','TRXUSDT','cfd_spot',true,'swaprate-test')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		// 原生键 spec facts（facts 写侧键域=venue 原生符号——生产实况镜像）
		{Kind: fact.KindSwapLong, Venue: "99990513", Symbol: "TRXUSD", Value: 22.5, Unit: fact.UnitBpsDaily, Ts: now.Add(-time.Hour)},
		// canonical 标签行兜底（D-325：无 map 行 venue 的 facts 键=canonical 本形）
		{Kind: fact.KindSwapLong, Venue: "99990514", Symbol: "TRX", Value: 18.0, Unit: fact.UnitBpsDaily, Ts: now.Add(-time.Hour)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	e := &Engine{store: s, quotes: bus.New(), cfg: gate.Config{}}

	// 主锚：裸形 canonical TRX×map 反解（TRXUSD）→ spec 命中。
	bps, src := e.swapRateFor(ctx, "99990513", "TRX", "BUY", 0)
	if src != swapSrcSpec || bps != 22.5 {
		t.Fatalf("反解命中: want spec 22.5, got %s %.2f", src, bps)
	}
	// 兜底锚：canonical 标签行（无 map 行→cands=[TRX]）→ spec 命中。
	bps2, src2 := e.swapRateFor(ctx, "99990514", "TRX", "BUY", 0)
	if src2 != swapSrcSpec || bps2 != 18.0 {
		t.Fatalf("canonical 兜底: want spec 18.0, got %s %.2f", src2, bps2)
	}
	// fail-closed 锚：真无 facts venue → none 语义保留。
	_, src3 := e.swapRateFor(ctx, "99990515", "TRX", "BUY", 0)
	if src3 != swapSrcNone {
		t.Fatalf("真无 facts 应 none, got %s", src3)
	}
}
