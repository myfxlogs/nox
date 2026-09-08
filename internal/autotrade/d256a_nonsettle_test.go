// D-256-A S1 单测：non-settle venue guard——swapRateFor 入口短路 (0, settled)。
// 用真实 DB（testAccumStore 模式），验证 non-settle 腿真值 0 + 双路径自动生效。
package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
)

// TestD256A_S1_NonSettleBareVenues lazy 构建集合：cfg 条目 "277842155:mt5" → bare "277842155"。
func TestD256A_S1_NonSettleBareVenues(t *testing.T) {
	e := &Engine{cfg: Config{SwapNonSettleVenues: []string{"277842155:mt5", "999:mt4"}}}
	m := e.nonSettleBareVenues()
	if !m["277842155"] {
		t.Errorf("bare 277842155 should be in set")
	}
	if !m["999"] {
		t.Errorf("bare 999 should be in set")
	}
	if len(m) != 2 {
		t.Errorf("set size: want 2, got %d", len(m))
	}
}

// TestD256A_S1_NonSettleBareVenues_Empty 未配置 → nil map（全放行）。
func TestD256A_S1_NonSettleBareVenues_Empty(t *testing.T) {
	e := &Engine{cfg: Config{}}
	m := e.nonSettleBareVenues()
	if m != nil {
		t.Errorf("empty config should yield nil map, got %v", m)
	}
	if e.isNonSettleBareVenue("anyVenue") {
		t.Errorf("empty set: any venue should not be non-settle")
	}
}

// TestD256A_S1_SwapRateFor_NonSettleVenue non-settle venue → (0, settled)，跳过 settled/spec 查询。
func TestD256A_S1_SwapRateFor_NonSettleVenue(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	// 写入 spec facts（正常 venue 会返回 spec 值；non-settle venue 应跳过）
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapShort, Venue: "277842155", Symbol: "GBPJPY", Value: 0.5, Unit: fact.UnitBpsDaily, Ts: now.Add(-1 * time.Hour)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	e := &Engine{store: s, quotes: bus.New(), cfg: Config{SwapNonSettleVenues: []string{"277842155:mt5"}}}
	bps, src := e.swapRateFor(ctx, "277842155", "GBPJPY", "SELL")
	if src != swapSrcSettled {
		t.Fatalf("source: want settled (non-settle=真值0), got %s", src)
	}
	if bps != 0 {
		t.Errorf("bps: want 0 (non-settle), got %.4f", bps)
	}
}

// TestD256A_S1_SwapRateForNet_OneNonSettleLeg 一腿 non-settle(0,settled) + 一腿 spec(0.5) → net=0.5, src=spec。
func TestD256A_S1_SwapRateForNet_OneNonSettleLeg(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	// vB 有 spec facts（SELL=0.5），vA 无 facts（non-settle 短路不查）
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: 0.5, Unit: fact.UnitBpsDaily, Ts: now.Add(-1 * time.Hour)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	e := &Engine{store: s, quotes: bus.New(), cfg: Config{SwapNonSettleVenues: []string{"vA:mt5"}}}
	net, src := e.swapRateForNet(ctx, "vA", "vB", "AUDJPY")
	// vA=non-settle→(0,settled), vB=spec→(0.5,spec) → net=0.5, src=spec（任一 spec→spec）
	if src != swapSrcSpec {
		t.Fatalf("source: want spec (mixed), got %s", src)
	}
	if net != 0.5 {
		t.Errorf("net: want 0.5 (0+0.5), got %.4f", net)
	}
}

// TestD256A_S1_SwapRateForNet_BothNonSettle 双 non-settle → net=0, src=settled（fail-closed: 0<门槛 skip）。
func TestD256A_S1_SwapRateForNet_BothNonSettle(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	e := &Engine{store: s, quotes: bus.New(), cfg: Config{SwapNonSettleVenues: []string{"vA:mt5", "vB:mt5"}}}
	net, src := e.swapRateForNet(ctx, "vA", "vB", "AUDJPY")
	if src != swapSrcSettled {
		t.Fatalf("source: want settled (both non-settle), got %s", src)
	}
	if net != 0 {
		t.Errorf("net: want 0 (both non-settle), got %.4f", net)
	}
}
