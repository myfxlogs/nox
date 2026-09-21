package autotrade

// D-327 锚：XASSET_CARRY entry spread 基准补齐。
// T2=backfillEntrySpread 白名单扩型（存量补齐+幂等）；T3=basisStopCheck 端到端武装
//（基准 NULL 不决策=485 缺陷形态，补基准后基差漂移穿阈值即平）。

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
)

// TestD327_BackfillEntrySpread_XAssetCarry T2：存量 XASSET_CARRY 缺基准仓回填补齐
// （96.58/96.594 → (96.58−96.594)/96.58×1e4 ≈ −1.4493，pos 485 真实形态）；
// 已有基准仓不被覆写（IS NULL 幂等）。
func TestD327_BackfillEntrySpread_XAssetCarry(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()

	// ① 缺基准：XASSET_CARRY 仓+双腿成交价（485 形态）→ backfill 补齐
	pos := d311SeedXAssetPosition(t, s, "XASSET_CARRY:CLUSDT:gate:perp:3001157:mt5", "gate:perp", "CL_USDT", "3001157:mt5", "USOILm")
	if _, err := s.Pool().Exec(ctx,
		`UPDATE position_legs SET price_actual = CASE side WHEN 'BUY' THEN 96.58 ELSE 96.594 END WHERE position_id = $1`, pos.ID); err != nil {
		t.Fatalf("seed price_actual: %v", err)
	}
	e := &Engine{store: s}
	e.backfillEntrySpread(ctx)

	var got *float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT expected_entry_spread_bps FROM positions WHERE id = $1`, pos.ID).Scan(&got); err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	if got == nil {
		t.Fatal("backfill 后 XASSET_CARRY 基准应非 NULL")
	}
	want := (96.58 - 96.594) / 96.58 * 10000
	if math.Abs(*got-want) > 1e-9 {
		t.Errorf("baseline = %.6f, want %.6f（成交价公式）", *got, want)
	}

	// ② 幂等：已有基准仓（−1.0）+ 不同腿价 → backfill 不覆写
	pos2 := d311SeedXAssetPosition(t, s, "XASSET_CARRY:XPTUSD:3001157:mt5:okx:perp", "3001157:mt5", "XPTUSD", "okx:perp", "XPT-USDT-SWAP")
	one := -1.0
	if _, err := s.Pool().Exec(ctx,
		`UPDATE position_legs SET price_actual = 100.0 WHERE position_id = $1`, pos2.ID); err != nil {
		t.Fatalf("seed price_actual 2: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`UPDATE positions SET expected_entry_spread_bps = $1 WHERE id = $2`, one, pos2.ID); err != nil {
		t.Fatalf("seed baseline: %v", err)
	}
	e.backfillEntrySpread(ctx)
	var got2 *float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT expected_entry_spread_bps FROM positions WHERE id = $1`, pos2.ID).Scan(&got2); err != nil {
		t.Fatalf("read baseline 2: %v", err)
	}
	if got2 == nil || math.Abs(*got2-(-1.0)) > 1e-9 {
		t.Errorf("已有基准不应被覆写, got %v, want -1.0", got2)
	}
}

// TestD327_BasisStopCheck_Armed T3 端到端（D-378b 改义）：XASSET 仓基准补齐后（entry=−0.145），
// 基差漂移至 −50.0（偏离 49.9 > 灾难带 40）→ checkExitCondition 返回平仓（退出链路武装证明；
// 窄阈 4bps 止损已撤，武装尺度改灾难带）；基准 NULL（485 缺陷形态）同行情 → 不决策（fail-closed 对照）。
func TestD327_BasisStopCheck_Armed(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	// funding=+10 健康（不触 funding 退出）→ 平仓只可能来自 basisStopCheck
	pos := d311XAssetExitFixture(t, s, 10.0)
	entry := -0.145
	if _, err := s.Pool().Exec(ctx,
		`UPDATE positions SET expected_entry_spread_bps = $1 WHERE id = $2`, entry, pos.ID); err != nil {
		t.Fatalf("seed baseline: %v", err)
	}
	pos.ExpectedEntrySpreadBps = &entry // basisStopCheck 读内存 struct（DB 列同步仅供查询一致性）

	// 报价：buy XPTUSD 3982/4000.2，sell XPTUSDT 4002/4002.02 → basis=(3982−4002.02)/4000.2×1e4 ≈ −50.04
	// （对 entry −0.145 偏离 49.9 > 灾难带 40）
	qb := bus.New()
	now := time.Now()
	qb.Publish(bus.Quote{Symbol: "XPTUSD", Venue: "3001157", VenueType: "mt5", Bid: 3982, Ask: 4000.2, Time: now, VenueSymbol: "XPTUSD"})
	qb.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "okx", VenueType: "perp", Bid: 4002, Ask: 4002.02, Time: now, VenueSymbol: "XPT-USDT-SWAP"})

	e := &Engine{
		store:         s,
		quotes:        qb,
		fundingBreach: map[int64]int{},
		cfg: Config{
			SwapFactMaxAge:       2 * time.Hour,
			FundingExitPct:       0,
			PerpFundMinHold:      0,
			PerpFundExitDebounce: 2,
			XAssetDisasterBps:    40, XAssetDisasterNoiseMult: 4, XAssetDisasterDebounce: 1, XAssetQuoteMaxAge: 5 * time.Minute,
		},
	}
	close, reason := e.checkExitCondition(context.Background(), pos)
	if !close {
		t.Fatalf("基差偏离穿灾难带应平仓（退出链路武装）, reason=%s", reason)
	}
	if !strings.HasPrefix(reason, "disaster") {
		t.Errorf("reason 应为 disaster 判定, got %q", reason)
	}

	// 对照：基准 NULL（485 缺陷形态）→ fail-closed 不决策
	d311Truncate(t, s)
	pos2 := d311XAssetExitFixture(t, s, 10.0) // 不设基准
	qb2 := bus.New()
	qb2.Publish(bus.Quote{Symbol: "XPTUSD", Venue: "3001157", VenueType: "mt5", Bid: 4000, Ask: 4000.2, Time: now, VenueSymbol: "XPTUSD"})
	qb2.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "okx", VenueType: "perp", Bid: 4002, Ask: 4002.02, Time: now, VenueSymbol: "XPT-USDT-SWAP"})
	e2 := &Engine{
		store:         s,
		quotes:        qb2,
		fundingBreach: map[int64]int{},
		cfg: Config{
			SwapFactMaxAge:       2 * time.Hour,
			FundingExitPct:       0,
			PerpFundMinHold:      0,
			PerpFundExitDebounce: 2,
			BasisStopBps:         4,
		},
	}
	if close, reason := e2.checkExitCondition(context.Background(), pos2); close {
		t.Errorf("基准 NULL 应 fail-closed 不决策（485 缺陷形态）, reason=%s", reason)
	}
}
