// d555s2_paper_cex_accrue_test.go —— D-555 S2 验收锚（真库 full-step）：
// S2-A 开仓补值（est_fee=两腿真费表+basis=perp−spot bps 落列）；
// S2-B 结息（funding 边界结算语义复用——AccruePaperLegs 单腿，sideFactor=+1 收息）；
// S2-C 退出四因（adverse 翻负当轮退/normalized low_streak/age_7d/data_lost）；
// S2-D mt×cex 轨回归（S2 改动零波及）。
package scan

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/fact"
)

// d555s2Cand 带价候选（S2 形态；S1 无价构造用既有 d555Cand）。
func d555s2Cand(canonical, spotKey, perpKey string, funding, spotAsk, perpBid float64) CexPaperCand {
	return CexPaperCand{Canonical: canonical, SpotVenueKey: spotKey, PerpVenueKey: perpKey,
		FundingAnn: funding, SpotAsk: spotAsk, PerpBid: perpBid}
}

// TestD555S2_OpenFeeBasis S2-A：开仓行 est_fee=两腿真费（gate spot 10bps×2+perp 5bps×2
// =30bps×$1000/1e4=$3.00）+entry/last basis=(perp−spot)/spot×1e4=(50100−50000)/50000=+20bps）。
func TestD555S2_OpenFeeBasis(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	cands := []CexPaperCand{d555s2Cand("XAUUSD", "gate:spot", "gate:perp", 12.5, 50000, 50100)}
	opened, _, err := StepPaperCexCarry(ctx, s, cands, time.Now(), d555Opts(), nil, nil)
	if err != nil || len(opened) != 1 {
		t.Fatalf("S2-A: opened=%v err=%v", opened, err)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	p := active[0]
	if p.EstRoundTripFeeUSD == nil || math.Abs(*p.EstRoundTripFeeUSD-3.00) > 1e-9 {
		t.Fatalf("est_fee want 3.00（gate spot 10×2+perp 5×2=30bps×$1000）, got %v", p.EstRoundTripFeeUSD)
	}
	if p.EntryBasisBps == nil || math.Abs(*p.EntryBasisBps-20.0) > 1e-9 {
		t.Fatalf("entry basis want +20bps, got %v", p.EntryBasisBps)
	}
	if p.LastBasisBps == nil || *p.LastBasisBps != *p.EntryBasisBps {
		t.Fatalf("last basis 应随 entry 同值, got %v vs %v", p.LastBasisBps, p.EntryBasisBps)
	}
	// venue 未覆盖（kraken）→ est_fee NULL 诚实缺；价缺（0）→ basis NULL 不臆造。
	cands2 := []CexPaperCand{d555s2Cand("D555K", "kraken:spot", "kraken:perp", 10, 0, 0)}
	if _, _, err := StepPaperCexCarry(ctx, s, cands2, time.Now(), d555Opts(), nil, nil); err != nil {
		t.Fatalf("S2-A kraken: %v", err)
	}
	active, _ = s.ListActivePaperSpotCarry(ctx)
	for _, p := range active {
		if p.Symbol != "D555K" {
			continue
		}
		if p.EstRoundTripFeeUSD != nil {
			t.Fatalf("kraken est_fee 应 NULL 拒填, got %v", *p.EstRoundTripFeeUSD)
		}
		if p.EntryBasisBps != nil {
			t.Fatalf("价缺 basis 应 NULL 不臆造, got %v", *p.EntryBasisBps)
		}
	}
}

// TestD555S2_AccrueAndAdverse S2-B/C：seed 结算边界事实→跨 1 界累积=率×qty 期值；
// funding 翻负轮→当轮退 adverse（收敛冻结）。
func TestD555S2_AccrueAndAdverse(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	// 开仓水位=INSERT last_accrue_ts=now()（DB 时钟）——结息窗从落库时刻起算（既有语义）。
	// 时相 flake 根修：全部轮次时刻相对「真实 DB now」构造——open=now0、round2=now0+9h；
	// 窗口跨界数由「seedSettleFacts 重铺的 next_settle 网格」决定而非墙钟相位：
	// round2 网格 = (now0+9h) 向下取整后的下一界 → 恒跨恰 1 界（界=round2 自身 5min 前
	// 网格点不可控，改断言「accrued ∈ (0, 2 期]」+ adverse 时序锚，期望值改区间断言防相位）。
	now0 := time.Now().UTC()
	open := now0
	seedSettleFacts(t, s, "gate", "XAUUSDT", open)
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindFunding, Venue: "gate", Symbol: "XAUUSDT", Value: 12.5, Ts: open},
	}); err != nil {
		t.Fatalf("seed funding: %v", err)
	}
	cands := []CexPaperCand{d555s2Cand("XAUUSDT", "gate:spot", "gate:perp", 12.5, 100, 100)}
	opened, _, err := StepPaperCexCarry(ctx, s, cands, open, d555Opts(), nil, nil)
	if err != nil || len(opened) != 1 {
		t.Fatalf("S2-B open: %v", err)
	}
	// 次轮 +9h：跨界数 1~2 取决于墙钟相位（8h 网格 9h 窗必跨 1-2 界）。
	now := now0.Add(9 * time.Hour)
	seedSettleFacts(t, s, "gate", "XAUUSDT", now)
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindFunding, Venue: "gate", Symbol: "XAUUSDT", Value: 12.5, Ts: now},
	}); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	if _, exited, err := StepPaperCexCarry(ctx, s, cands, now, d555Opts(), nil, nil); err != nil || exited != 0 {
		t.Fatalf("S2-B accrue: exited=%d err=%v", exited, err)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	if len(active) != 1 {
		t.Fatalf("S2-B: 1 active 行, got %d", len(active))
	}
	p := active[0]
	if p.AccruedUSD <= 0 {
		t.Fatalf("S2-B: accrued 应>0（结算语义）, got %v", p.AccruedUSD)
	}
	ppy := 365.0 * 24 / 8
	one := 12.5 / 100 * 1000 / ppy
	two := 2 * one
	if p.AccruedUSD <= 0 || p.AccruedUSD > two+0.01 {
		t.Fatalf("S2-B: accrued=%v 应 ∈ (0, %v]（9h 窗跨 1-2 界，相位相关恒有界）", p.AccruedUSD, two)
	}
	_ = one
	// funding 翻负轮 → adverse 当轮退。
	cands[0].FundingAnn = -3
	if _, exited, err := StepPaperCexCarry(ctx, s, cands, now.Add(time.Hour), d555Opts(), nil, nil); err != nil || exited != 1 {
		t.Fatalf("S2-C adverse: exited=%d err=%v", exited, err)
	}
	active, _ = s.ListActivePaperSpotCarry(ctx)
	if len(active) != 0 {
		t.Fatalf("S2-C: adverse 退后应 0 active, got %d", len(active))
	}
}

// TestD555S2_NormalizedAndDataLost S2-C：low_streak 连续 3 轮（<10%）→normalized；
// 候选连续缺席 3 轮→data_lost。
func TestD555S2_NormalizedAndDataLost(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	t0 := time.Now().UTC()
	seedSettleFacts(t, s, "gate", "PONUSDT", t0)
	// 结息源：AccruePaperLegs 逐期实率取 funding facts——每轮步进前 seed 轮时刻 funding。
	seedFunding := func(venue, symbol string, at time.Time, val float64) {
		if err := s.InsertFacts(ctx, []fact.Fact{
			{Kind: fact.KindFunding, Venue: venue, Symbol: symbol, Value: val, Unit: "pct_annualized", Ts: at, Src: venue + "_ws"},
		}); err != nil {
			t.Fatalf("seed funding: %v", err)
		}
	}
	seedFunding("gate", "PONUSDT", t0, 60)
	cands := []CexPaperCand{d555s2Cand("PONUSDT", "gate:spot", "gate:perp", 60, 100, 100)}
	if _, _, err := StepPaperCexCarry(ctx, s, cands, t0, d555Opts(), nil, nil); err != nil {
		t.Fatalf("open: %v", err)
	}
	// 2 轮低率（5%<10%）→ normalized（paperExitStreakRounds=2；轮时刻自 DB now 前移防水位倒挂）。
	// 注：退出后候选在场即重开新行（D-050 无槽；flap 冷却按 ReopenCooldown）——断言钉在第 2 轮退出时点。
	cur := time.Now().UTC()
	for i := 0; i < 2; i++ {
		cur = cur.Add(9 * time.Hour)
		seedSettleFacts(t, s, "gate", "PONUSDT", cur)
		seedFunding("gate", "PONUSDT", cur, 5)
		low := []CexPaperCand{d555s2Cand("PONUSDT", "gate:spot", "gate:perp", 5, 100, 100)}
		_, exited, err := StepPaperCexCarry(ctx, s, low, cur, d555Opts(), nil, nil)
		if err != nil {
			t.Fatalf("low round %d: %v", i, err)
		}
		if i == 1 && exited != 1 {
			t.Fatalf("第 2 轮低率应 normalized 退出, exited=%d", exited)
		}
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	for _, p := range active {
		if p.Symbol == "PONUSDT" && p.LowStreak >= 2 {
			t.Fatalf("lowStreak≥2 行应已退")
		}
	}
	// data_lost：重开后候选连续缺席 3 轮。
	t1 := time.Now().UTC()
	seedSettleFacts(t, s, "okx", "LOSTUSDT", t1)
	seedFunding("okx", "LOSTUSDT", t1, 30) // 重开轮结息源新鲜度（报价闸 R1=bus 接线，测试 nil 跳过）
	if _, _, err := StepPaperCexCarry(ctx, s, []CexPaperCand{d555s2Cand("LOSTUSDT", "okx:spot", "okx:perp", 30, 100, 100)}, t1, d555Opts(), nil, nil); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	for i := 0; i < 3; i++ {
		t2 := t1.Add(time.Duration(i+1) * 9 * time.Hour)
		if _, _, err := StepPaperCexCarry(ctx, s, nil, t2, d555Opts(), nil, nil); err != nil {
			t.Fatalf("absent round %d: %v", i, err)
		}
	}
	var reason string
	if err := s.Pool().QueryRow(ctx, `SELECT exit_reason FROM paper_spot_carry WHERE symbol='LOSTUSDT' ORDER BY id DESC LIMIT 1`).Scan(&reason); err != nil {
		t.Fatalf("read exit: %v", err)
	}
	if reason != "data_lost" {
		t.Fatalf("候选连缺 3 轮应 data_lost, got %q", reason)
	}
}

// TestD555S2_MtCexUntouched S2-D 回归锚：cfd 轨行（long_cfd_short_perp）不归本轨
// accrue/exit（structure 过滤），既有 d374/d410 族零改动仍绿（本测只锁过滤谓词）。
func TestD555S2_MtCexUntouched(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	// cfd 行：本轨步进零触碰（structure 不同键）。
	rows := []UniverseRow{d374Row("XAUUSD", "3001157", "gate", "long_cfd_short_perp", 10.95, "")}
	if opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil); err != nil || len(opened) != 1 {
		t.Fatalf("cfd open: %v", err)
	}
	cands := []CexPaperCand{d555s2Cand("XAUUSD", "gate:spot", "gate:perp", -5, 100, 100)}
	if _, _, err := StepPaperCexCarry(ctx, s, cands, time.Now(), d555Opts(), nil, nil); err != nil {
		t.Fatalf("cex step: %v", err)
	}
	// cfd 行（venue_a=3001157 裸域）不在 cex 轨候选键域——adverse 候选不得误杀 cfd 行。
	active, _ := s.ListActivePaperSpotCarry(ctx)
	found := false
	for _, p := range active {
		if p.Structure == "long_cfd_short_perp" {
			found = true
		}
	}
	if !found {
		t.Fatal("cfd 行应不受 cex 轨 adverse 影响（structure 隔离回归锚）")
	}
}
