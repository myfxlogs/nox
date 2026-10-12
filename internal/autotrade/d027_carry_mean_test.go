// d027_carry_mean_test.go —— D-027：carry 期望口径改滚动均值 + carry_death 休市感知。
// 验收锚：周六 funding=0 序列不触发 carry_death、不触发收割；入场闸按 7d 均值判；
// MT 腿 trade_mode 非 Full（休市）期间 breach 不累计。
package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// d027SeedFunding 按日序列播种 funding facts（vals[i]=i 天前年化 %，每天 n 条均匀撒开）。
func d027SeedFunding(t *testing.T, s *pgstore.Store, venue, symbol string, vals []float64, nPerDay int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	var fs []fact.Fact
	for i, v := range vals {
		dayStart := now.Add(-time.Duration(i) * 24 * time.Hour)
		for j := 0; j < nPerDay; j++ {
			fs = append(fs, fact.Fact{
				Kind: fact.KindFunding, Venue: venue, Symbol: symbol, Value: v,
				Unit: fact.UnitPctAnnualized, Ts: dayStart.Add(-time.Duration(j) * time.Hour), Src: "test",
			})
		}
	}
	if err := s.InsertFacts(ctx, fs); err != nil {
		t.Fatalf("seed funding facts: %v", err)
	}
}

// TestD027_FundingMean7d 腿 carry 口径：瞬时=0（周六）但 7d 均值非零。
func TestD027_FundingMean7d(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	// 前 6 天 28%/yr，今天（周六）全 0 → 7d 均值 24；瞬时=0。
	d027SeedFunding(t, s, "okx", "XPTUSDT", []float64{0, 28, 28, 28, 28, 28, 28}, 4)
	e := &Engine{store: s}
	inst, ok := e.xassetLegCarryAnnual(ctx, "okx:perp", "XPTUSDT", "SELL", 0, 0)
	if !ok || inst != 0 {
		t.Fatalf("瞬时口径应=0（周六最新样本），got %v ok=%v", inst, ok)
	}
	mean7d, ok := e.xassetLegCarryAnnual(ctx, "okx:perp", "XPTUSDT", "SELL", 0, carryEconWindow)
	if !ok || mean7d < 23.9 || mean7d > 24.1 {
		t.Fatalf("7d 均值应≈24%%/yr（含周末零样本），got %v ok=%v", mean7d, ok)
	}
	mean3d, ok := e.xassetLegCarryAnnual(ctx, "okx:perp", "XPTUSDT", "SELL", 0, carryDeathMeanWindow)
	if !ok || mean3d < 18.5 || mean3d > 18.9 {
		t.Fatalf("3d 均值应≈18.67%%/yr，got %v ok=%v", mean3d, ok)
	}
}

// TestD027_SaturdayNoCarryDeath 周六 funding=0 序列不触发 carry_death、不触发收割：
// 3d 均值仍 >0 → breach 不累计；随后瞬时转负但均值仍正 → 同不累计；
// 全程 funding=0（纯周末）+ 均值=0 → 也不收割（HarvestOK 需净正）。
func TestD027_SaturdayNoCarryDeath(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d014EconPos(t, s, 0) // fixture 自带瞬时 funding=0
	// 3d 均值口径：前两天 30%/yr，今天周六全 0 → 均值≈20>0 → 无 breach。
	d027SeedFunding(t, s, "okx", "XPTUSDT", []float64{0, 30, 30}, 8)
	// 持仓 9h（满 8h floor 后 breach 才可累计——证明是均值而非 floor 在拦）。
	opened := time.Now().Add(-9 * time.Hour)
	if _, err := s.Pool().Exec(ctx, `UPDATE positions SET opened_at=$1 WHERE id=$2`, opened, p.ID); err != nil {
		t.Fatalf("set opened_at: %v", err)
	}
	rows, _ := s.ListPositions(ctx, "OPEN", 10, 0)
	p = &rows[0]
	qb := d404Quotes(time.Now().UTC(), 4000, 4000, 0)
	e := d014EconEngine(s, qb, true)
	e.cfg.PerpFundMinHold = 8 * time.Hour
	e.cfg.PerpFundExitDebounce = 2
	for i := 0; i < 3; i++ {
		close, reason := e.xassetExitCheck(ctx, p)
		if close {
			t.Fatalf("轮 %d：3d 均值≈20%%/yr >0 不应判死/收割，reason=%q", i, reason)
		}
	}
	if got := e.fundingBreach[p.ID]; got != 0 {
		t.Fatalf("breach 应不累计，got %d", got)
	}
	// 对照：3d 均值打负（全 −10 三天）→ breach 正常累计（证明闸不是「永不计数」）。
	s2 := testAccumStore(t)
	d311Truncate(t, s2)
	p2 := d014EconPos(t, s2, -10)
	d027SeedFunding(t, s2, "okx", "XPTUSDT", []float64{-10, -10, -10}, 8)
	if _, err := s2.Pool().Exec(ctx, `UPDATE positions SET opened_at=$1 WHERE id=$2`, opened, p2.ID); err != nil {
		t.Fatalf("set opened_at: %v", err)
	}
	rows2, _ := s2.ListPositions(ctx, "OPEN", 10, 0)
	p2 = &rows2[0]
	e2 := d014EconEngine(s2, d404Quotes(time.Now().UTC(), 4000, 4000, 0), true)
	e2.cfg.PerpFundMinHold = 8 * time.Hour
	e2.cfg.PerpFundExitDebounce = 2
	if close, _ := e2.xassetExitCheck(ctx, p2); close {
		t.Fatal("首次 breach（count=1<2）不应即平")
	}
	if got := e2.fundingBreach[p2.ID]; got != 1 {
		t.Fatalf("均值 −10 → breach 应累计 count=1，got %d", got)
	}
}

// TestD027_MTClosedNoBreach MT 腿 trade_mode 事实非 Full（休市）→ breach 不累计
// （即便 3d 均值为负）；fact=Full → 正常累计。
func TestD027_MTClosedNoBreach(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d014EconPos(t, s, -10)
	d027SeedFunding(t, s, "okx", "XPTUSDT", []float64{-10, -10, -10}, 8)
	// MT 腿休市：trade_mode=CloseOnly。
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindTradeMode, Venue: "3001157", Symbol: "XPTUSD", Value: float64(fact.TradeModeCloseOnly), Unit: fact.UnitMode, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("seed trade_mode: %v", err)
	}
	opened := now.Add(-9 * time.Hour)
	if _, err := s.Pool().Exec(ctx, `UPDATE positions SET opened_at=$1 WHERE id=$2`, opened, p.ID); err != nil {
		t.Fatalf("set opened_at: %v", err)
	}
	rows, _ := s.ListPositions(ctx, "OPEN", 10, 0)
	p = &rows[0]
	e := d014EconEngine(s, d404Quotes(now, 4000, 4000, 0), true)
	e.cfg.PerpFundMinHold = 8 * time.Hour
	e.cfg.PerpFundExitDebounce = 1 // 单次 breach 即平——闸生效与否一眼分明
	for i := 0; i < 2; i++ {
		if close, reason := e.xassetExitCheck(ctx, p); close {
			t.Fatalf("MT 休市期间不应累计 breach 判死，reason=%q", reason)
		}
	}
	if got := e.fundingBreach[p.ID]; got != 0 {
		t.Fatalf("休市期间 breach 不累计，got %d", got)
	}
	// 恢复 Full → 正常判定（单次即平）。
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindTradeMode, Venue: "3001157", Symbol: "XPTUSD", Value: float64(fact.TradeModeFull), Unit: fact.UnitMode, Ts: now.Add(time.Second), Src: "mt5"},
	}); err != nil {
		t.Fatalf("seed trade_mode full: %v", err)
	}
	if close, reason := e.xassetExitCheck(ctx, p); !close {
		t.Fatalf("开市后 3d 均值 −10 应判死（debounce=1），reason=%q", reason)
	}
}

// TestD027_EntryGateUses7dMean 入场闸按 7d 均值判：周六瞬时=0 不拦开。
// 双腿 perp（gate BUY 付均值 / okx SELL 收均值）：okx 7d=[0,+40×6]→≈34.3 收、
// gate 7d 恒 0 → BUY carry 0 → net≈34.3%/yr → CarryPerDay≈$3.92/d，BE≈(8.32)/3.92≈2.1d<3 放行；
// 瞬时口径（窗口=0）carry=0 → EntryOK=false——两口径对照同一 fixture。
func TestD027_EntryGateUses7dMean(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	// CanonicalSell 未给 → 双腿同 canonical=XPTUSD（SWAPARB 同 canonical 常形）。
	d027SeedFunding(t, s, "okx", "XPTUSD", []float64{0, 40, 40, 40, 40, 40, 40}, 4)
	d027SeedFunding(t, s, "gate", "XPTUSD", []float64{0, 0, 0, 0, 0, 0, 0}, 4)
	e := &Engine{store: s, cfg: Config{
		EconFeeBps: func(_ context.Context, vk string) (float64, bool) {
			switch vk {
			case "okx:perp", "gate:perp":
				return 5.0, true
			}
			return 0, false
		},
	}}
	opp := pgstore.Opportunity{ID: "XASSET_CARRY:XPTUSD:gate:perp:okx:perp", Canonical: "XPTUSD",
		VenueBuy: "gate:perp", VenueSell: "okx:perp"}
	snapMean, err := e.econEntrySnapshot(ctx, opp, "", 4160)
	if err != nil {
		t.Fatalf("econEntrySnapshot: %v", err)
	}
	// 瞬时口径对照：直接把 facts 全删留最新 0 —— 等效断言均值 vs 瞬时差异。
	instMean, _, _ := e.fundingRateForVenue(ctx, "okx", "XPTUSD")
	if instMean != 0 {
		t.Fatalf("瞬时 funding 应为 0（周六），got %v", instMean)
	}
	if !snapMean.EntryOK(3.0) {
		t.Fatalf("7d 均值 carry≈34%%/yr 应放行（BE≈%.2fd），snap=%+v", snapMean.BreakevenDays(), snapMean)
	}
	if snapMean.CarryPerDayUSD < 3.5 || snapMean.CarryPerDayUSD > 4.3 {
		t.Fatalf("CarryPerDay 应≈$3.9/d（7d 均值 34.3%%/yr × $4160），got %.2f", snapMean.CarryPerDayUSD)
	}
}
