package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/data/fact"
	"arb/internal/platform/combo"
	"arb/internal/risk/gate"
)

// TestScanSwapArb_DisabledStillFeedsRollup R8.2（R3 欠账）：判死策略停扫改停提交后，
// 禁用态 SWAPARB 仍喂 rollup（扫描+评估常开）且 autotrade_logs 零新增（不进 gate 无 REJECTED 行）。
// DSN：seed facts 两行 → DisableStrategy → scanSwapArb+Flush → 断言 rollup 有行 + logs 零新增。
func TestScanSwapArb_DisabledStillFeedsRollup(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	// 清表：facts/autotrade_logs/combo_daily_rollup/strategy_stats
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, autotrade_logs, combo_daily_rollup, strategy_stats`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedDemoAccounts(t, s, "vA", "vB") // D-144 ① 三态：补 broker_accounts 行（demo）
	// seed facts：swap_long AUDJPY vA=+5 / swap_short AUDJPY vB=−3 → net=+2 bps
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	// 禁用 SWAPARB
	if err := s.DisableStrategy(ctx, "SWAPARB", "test disabled"); err != nil {
		t.Fatalf("disable strategy: %v", err)
	}
	// autotrade_logs 基线（清表后=0）
	var baseline int
	if err := s.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM autotrade_logs`).Scan(&baseline); err != nil {
		t.Fatalf("count logs baseline: %v", err)
	}
	// 构造 Engine：quotes=nil 可行——disabled 检查在 quotes.Latest 之前 return
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0}}
	acc := newComboAccumulator()
	e.scanSwapArb(ctx, acc)
	if err := acc.Flush(ctx, s); err != nil {
		t.Fatalf("flush: %v", err)
	}
	// 断言 1：rollup 有该组合行（samples=1）
	ck := combo.Key("SWAPARB", "AUDJPY", "vA:mt5", "vB:mt5")
	rows, err := s.ListComboDaily(ctx, ck, 10)
	if err != nil {
		t.Fatalf("ListComboDaily: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 rollup row, got %d", len(rows))
	}
	r := rows[0]
	if r.Samples != 1 {
		t.Errorf("want samples=1, got %d", r.Samples)
	}
	if absFloat(r.NetAvg-2.0) > 0.0001 {
		t.Errorf("want net_avg=2.0, got %v", r.NetAvg)
	}
	// 断言 2：autotrade_logs 零新增（禁用态不进 gate，无 REJECTED 行）
	var after int
	if err := s.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM autotrade_logs`).Scan(&after); err != nil {
		t.Fatalf("count logs after: %v", err)
	}
	if after != baseline {
		t.Errorf("want autotrade_logs unchanged (baseline=%d), got %d (新增 %d 行)", baseline, after, after-baseline)
	}
}

// TestScanSwapArb_CarryDisabledStillFeedsRollup D-187：CARRY 合并 SWAPARB 统一路径——
// 高 netSwap 组合被标 CARRY（annualPct ≥ CarryMinNetAnnualized），禁用 CARRY 策略后
// 仍喂 rollup（扫描+评估常开）且 autotrade_logs 零新增（trySwapArb 用 strategy 参数查禁用）。
// 证明：① discoverSwapCombos 标 strategy="CARRY" ② scanSwapArb 用 c.strategy ③ trySwapArb
// IsStrategyDisabled(ctx, strategy) 用传入的 strategy 而非硬编码 "SWAPARB"。
func TestScanSwapArb_CarryDisabledStillFeedsRollup(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, autotrade_logs, combo_daily_rollup, strategy_stats`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedDemoAccounts(t, s, "vA", "vB")
	// seed facts：swap_long AUDJPY vA=+5 / swap_short AUDJPY vB=−3 → net=+2 bps
	// annualPct=2*365/100=7.3 ≥ CarryMinNetAnnualized=4.0 → strategy="CARRY"
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	// 禁用 CARRY（非 SWAPARB）——证明 trySwapArb 用 strategy 参数查禁用
	if err := s.DisableStrategy(ctx, "CARRY", "test disabled"); err != nil {
		t.Fatalf("disable strategy: %v", err)
	}
	var baseline int
	if err := s.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM autotrade_logs`).Scan(&baseline); err != nil {
		t.Fatalf("count logs baseline: %v", err)
	}
	// CarryMinNetAnnualized=4.0 → netSwap=2.0 标 CARRY；quotes=nil 可行——disabled 检查在 quotes.Latest 之前 return
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, CarryMinNetAnnualized: 4.0}}
	acc := newComboAccumulator()
	e.scanSwapArb(ctx, acc)
	if err := acc.Flush(ctx, s); err != nil {
		t.Fatalf("flush: %v", err)
	}
	// 断言 1：rollup 有该组合行，combo key 前缀为 CARRY（非 SWAPARB）
	ck := combo.Key("CARRY", "AUDJPY", "vA:mt5", "vB:mt5")
	rows, err := s.ListComboDaily(ctx, ck, 10)
	if err != nil {
		t.Fatalf("ListComboDaily: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 rollup row with CARRY prefix, got %d", len(rows))
	}
	if rows[0].Samples != 1 {
		t.Errorf("want samples=1, got %d", rows[0].Samples)
	}
	// 断言 2：autotrade_logs 零新增（CARRY 禁用 → trySwapArb 早退，不进 gate）
	var after int
	if err := s.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM autotrade_logs`).Scan(&after); err != nil {
		t.Fatalf("count logs after: %v", err)
	}
	if after != baseline {
		t.Errorf("want autotrade_logs unchanged (baseline=%d), got %d (新增 %d 行)", baseline, after, after-baseline)
	}
}
