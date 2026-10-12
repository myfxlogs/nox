package autotrade

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/risk/gate"
)

// TestDiscoverSwapCombos_FreshnessGate_FreshPass D-104 P0：新鲜 facts 通过门禁。
func TestDiscoverSwapCombos_FreshnessGate_FreshPass(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedDemoAccounts(t, s, "vA", "vB") // D-144 ① 三态：补 broker_accounts 行（demo）
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
	combos := e.discoverSwapCombos(ctx)
	if len(combos) != 1 {
		t.Fatalf("fresh facts should pass gate: want 1 combo, got %d", len(combos))
	}
	if combos[0].netSwapBps != 2.0 {
		t.Errorf("want netSwapBps=2.0, got %v", combos[0].netSwapBps)
	}
}

// TestDiscoverSwapCombos_FreshnessGate_StaleSkip D-104 P0：陈旧 facts 被门禁跳过（fail-closed）。
func TestDiscoverSwapCombos_FreshnessGate_StaleSkip(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	staleTs := now.Add(-3 * time.Hour) // > 2h maxAge
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: staleTs, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: staleTs, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
	combos := e.discoverSwapCombos(ctx)
	if len(combos) != 0 {
		t.Fatalf("stale facts should be skipped by gate: want 0 combos, got %d", len(combos))
	}
}

// TestDiscoverSwapCombos_FreshnessGate_Mixed D-104 P0：混合新鲜+陈旧 → 只新鲜参与发现。
func TestDiscoverSwapCombos_FreshnessGate_Mixed(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedDemoAccounts(t, s, "vA", "vB", "vC", "vD") // D-144 ① 三态：补 broker_accounts 行（demo）
	now := time.Now().UTC()
	staleTs := now.Add(-3 * time.Hour)
	// vA long fresh, vC long stale; vB short fresh, vD short stale
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "vC", Symbol: "AUDJPY", Value: 8.0, Unit: fact.UnitBpsDaily, Ts: staleTs, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vD", Symbol: "AUDJPY", Value: -1.0, Unit: fact.UnitBpsDaily, Ts: staleTs, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
	combos := e.discoverSwapCombos(ctx)
	// 只 vA×vB 通过（vC/vD 陈旧被跳过）
	if len(combos) != 1 {
		t.Fatalf("mixed: want 1 combo (vA×vB fresh), got %d", len(combos))
	}
	if combos[0].vLong != "vA:mt5" || combos[0].vShort != "vB:mt5" {
		t.Errorf("want vA:mt5 × vB:mt5, got %s × %s", combos[0].vLong, combos[0].vShort)
	}
}

// TestDiscoverSwapCombos_FreshnessGate_Disabled D-104 P0：SwapFactMaxAge=0 → 门禁关闭（全扫语义）。
func TestDiscoverSwapCombos_FreshnessGate_Disabled(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedDemoAccounts(t, s, "vA", "vB") // D-144 ① 三态：补 broker_accounts 行（demo）
	now := time.Now().UTC()
	staleTs := now.Add(-24 * time.Hour)
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: staleTs, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: staleTs, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, SwapFactMaxAge: 0}} // 门禁关闭
	combos := e.discoverSwapCombos(ctx)
	if len(combos) != 1 {
		t.Fatalf("gate disabled: stale facts should pass (full scan), want 1 combo, got %d", len(combos))
	}
}
