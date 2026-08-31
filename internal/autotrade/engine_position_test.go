package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// seedSwapPosition 建一条 SWAPARB 持仓 + 双腿（BUY vA:mt5 / SELL vB:mt5），oppID canonical=AUDJPY。
// D-105 T1 recalcNetSwap/recalcNetFunding fail-closed 单测共用。
func seedSwapPosition(t *testing.T, s *pgstore.Store) *pgstore.Position {
	t.Helper()
	ctx := context.Background()
	pos := &pgstore.Position{
		OpportunityID: "SWAPARB:AUDJPY:vA:vB:1700000000000",
		Type:          "SWAPARB",
		Status:        "OPEN",
	}
	id, err := s.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("create position: %v", err)
	}
	pos.ID = id
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 1, Phase: "OPEN", Venue: "vA:mt5", Symbol: "AUDJPY",
		Side: "BUY", Qty: 1.0, Status: "FILLED",
	}); err != nil {
		t.Fatalf("create leg1: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 2, Phase: "OPEN", Venue: "vB:mt5", Symbol: "AUDJPY",
		Side: "SELL", Qty: 1.0, Status: "FILLED",
	}); err != nil {
		t.Fatalf("create leg2: %v", err)
	}
	return pos
}

// TestRecalcNetSwap_NormalBothVenues D-105 T1：双 venue 新鲜 fact → 现值回归（true）。
func TestRecalcNetSwap_NormalBothVenues(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	pos := seedSwapPosition(t, s)
	e := &Engine{store: s}
	got, ok := e.recalcNetSwap(ctx, pos)
	if !ok {
		t.Fatalf("normal both venues: want ok=true, got false")
	}
	want := 2.0 // 5.0 + (-3.0)
	if got != want {
		t.Errorf("recalcNetSwap want %.4f, got %.4f", want, got)
	}
}

// TestRecalcNetSwap_MissingVenue D-105 T1：单 venue fact 缺失 → (0,false) 不决策平仓（fail-closed）。
// 修复 #264 误平根因：旧代码静默取 0 → 0+0=0≤0 假信号平仓。
func TestRecalcNetSwap_MissingVenue(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	// 只有 swap_long vA，缺 swap_short vB（模拟测试截断 facts → venue 缺失）。
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vX", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	pos := seedSwapPosition(t, s)
	e := &Engine{store: s}
	got, ok := e.recalcNetSwap(ctx, pos)
	if ok {
		t.Errorf("missing venue: want ok=false (fail-closed, no decision), got ok=true val=%.4f", got)
	}
	if got != 0 {
		t.Errorf("missing venue: want val=0, got %.4f", got)
	}
}

// TestRecalcNetSwap_StaleMatchedFact D-105 T1：匹配到的 fact 陈旧>48h → (0,false)。
// 修复：旧代码查 longFacts[0] 列表首元素（非该 venue 的 fact），匹配后新鲜度未真正检查。
func TestRecalcNetSwap_StaleMatchedFact(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	stale := now.Add(-49 * time.Hour) // >48h
	// vA/vB 匹配的 fact 陈旧；另插一行新鲜但非匹配 venue（vC），验证守卫查的是匹配 fact 不是首元素。
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vC", Symbol: "AUDJPY", Value: 9.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: stale, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: stale, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	pos := seedSwapPosition(t, s)
	e := &Engine{store: s, cfg: Config{SwapFactMaxAge: 2 * time.Hour}} // D-159 W4a R7：TTL 统一用 cfg.SwapFactMaxAge
	got, ok := e.recalcNetSwap(ctx, pos)
	if ok {
		t.Errorf("stale matched fact: want ok=false, got ok=true val=%.4f", got)
	}
}

// --- recalcNetFunding 同构三 case ---

func seedFundingFacts(t *testing.T, s *pgstore.Store, vAVal, vBVal float64, vATs, vBTs time.Time, includeVB bool) {
	t.Helper()
	ctx := context.Background()
	facts := []fact.Fact{
		{Kind: fact.KindFunding, Venue: "vA", Symbol: "AUDJPY", Value: vAVal, Unit: fact.UnitPctAnnualized, Ts: vATs, Src: "mt5"},
	}
	if includeVB {
		facts = append(facts, fact.Fact{
			Kind: fact.KindFunding, Venue: "vB", Symbol: "AUDJPY", Value: vBVal, Unit: fact.UnitPctAnnualized, Ts: vBTs, Src: "mt5",
		})
	}
	if err := s.InsertFacts(ctx, facts); err != nil {
		t.Fatalf("insert funding facts: %v", err)
	}
}

// TestRecalcNetFunding_NormalBothVenues D-105 T1：双 venue 新鲜 → (long−short, true)。
func TestRecalcNetFunding_NormalBothVenues(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	seedFundingFacts(t, s, 10.0, 4.0, now, now, true)
	pos := seedSwapPosition(t, s)
	e := &Engine{store: s}
	got, ok := e.recalcNetFunding(ctx, pos)
	if !ok {
		t.Fatalf("normal: want ok=true, got false")
	}
	want := 6.0 // 10 − 4
	if got != want {
		t.Errorf("recalcNetFunding want %.4f, got %.4f", want, got)
	}
}

// TestRecalcNetFunding_MissingVenue D-105 T1：缺 vB funding → (0,false) fail-closed。
func TestRecalcNetFunding_MissingVenue(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	seedFundingFacts(t, s, 10.0, 0, now, now, false) // 不插 vB
	pos := seedSwapPosition(t, s)
	e := &Engine{store: s}
	got, ok := e.recalcNetFunding(ctx, pos)
	if ok {
		t.Errorf("missing venue: want ok=false, got ok=true val=%.4f", got)
	}
}

// TestRecalcNetFunding_StaleMatchedFact D-105 T1：匹配 fact 陈旧>48h → (0,false)。
func TestRecalcNetFunding_StaleMatchedFact(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	stale := now.Add(-49 * time.Hour)
	seedFundingFacts(t, s, 10.0, 4.0, stale, stale, true)
	pos := seedSwapPosition(t, s)
	e := &Engine{store: s, cfg: Config{SwapFactMaxAge: 2 * time.Hour}} // D-159 W4a R7：TTL 统一用 cfg.SwapFactMaxAge
	got, ok := e.recalcNetFunding(ctx, pos)
	if ok {
		t.Errorf("stale: want ok=false, got ok=true val=%.4f", got)
	}
}
