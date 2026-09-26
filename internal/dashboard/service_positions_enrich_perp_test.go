package dashboard

// D-146 S5 微件②单测：PERP_FUNDING 三列（预期/实收/达成）计算 + SWAPARB 不变（回归锚 #456）。

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestPerpFundingExpectedUSD 纯函数：年化 expected_net_bps 按持有天数折算应得 USD。
// expected_net_bps=5.0（5% 年化）, notional=10000, holdDays=365 → 500 USD（满年）。
// holdDays=36.5 → 50 USD（1/10 年）。任一因子 ≤0 → 0。
func TestPerpFundingExpectedUSD(t *testing.T) {
	cases := []struct {
		name           string
		expectedNetBps float64
		notionalUSD    float64
		holdDays       float64
		want           float64
	}{
		{"满年 5%", 5.0, 10000, 365, 500.0},
		{"1/10 年 5%", 5.0, 10000, 36.5, 50.0},
		{"10% 年化 30 天", 10.0, 10000, 30, 10000.0 * 10 / 100 * 30 / 365},
		{"bps=0 → 0", 0, 10000, 30, 0},
		{"notional=0 → 0", 5.0, 0, 30, 0},
		{"holdDays=0 → 0", 5.0, 10000, 0, 0},
		{"负 bps → 0", -1, 10000, 30, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := perpFundingExpectedUSD(c.expectedNetBps, c.notionalUSD, c.holdDays)
			if math.Abs(got-c.want) > 0.001 {
				t.Errorf("perpFundingExpectedUSD(%.1f, %.1f, %.1f) = %.4f, want %.4f",
					c.expectedNetBps, c.notionalUSD, c.holdDays, got, c.want)
			}
		})
	}
}

// TestEnrichOne_PerpFundingThreeColsSkipStoreNil PERP_FUNDING + store=nil → notional=0 → 三列缺省（诚实不猜）。
// 验证 PERP_FUNDING 分支不 panic + store=nil 路径仍诚实缺省。
func TestEnrichOne_PerpFundingThreeColsSkipStoreNil(t *testing.T) {
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "binance:spot", Symbol: "BTCUSDT", Pnl: -10, Swap: 0, PositionId: 555},
	})
	s := newEnrichService(bpm) // store=nil → notional=0
	pb := &v1.Position{
		Id: 555, OpportunityId: "PERP_FUNDING:BTCUSDT:binance:spot:okx:perp",
		Type: "PERP_FUNDING", Status: "OPEN", ExpectedNetBps: 5.0,
		OpenedAt: timestamppb.New(time.Now().AddDate(0, 0, -3)),
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "binance:spot", Symbol: "BTCUSDT", Status: "FILLED", Qty: 0.01, PriceActual: ptrFloat(60000), Ts: time.Now().AddDate(0, 0, -3)},
	}
	now := time.Now()
	fundingByPos := map[string]float64{"555\x00binance:spot": 3.0}
	enrichOne(context.Background(), s, pb, legs, bpm.Snapshot(), now, map[string]time.Weekday{}, fundingByPos, map[int64]float64{})

	// store=nil → notional=0 → expected_swap_usd_to_date 应 nil（诚实缺省）
	if pb.ExpectedSwapUsdToDate != nil {
		t.Errorf("store=nil notional=0 → expected_swap_usd_to_date 应 nil, got %v", *pb.ExpectedSwapUsdToDate)
	}
	if pb.AttainmentPct != nil {
		t.Errorf("应得缺省 → attainment_pct 应 nil, got %v", *pb.AttainmentPct)
	}
	// 但 swapAccrued 仍出值（S5 funding 并入，matched=true）
	if pb.SwapAccruedUsd == nil || math.Abs(*pb.SwapAccruedUsd-3.0) > 0.001 {
		t.Errorf("swap_accrued = %v, want 3.0 (funding merged even if expected nil)", pb.SwapAccruedUsd)
	}
}

// TestEnrichOne_SwapARBRegressionNoPerpFundingBranch SWAPARB 不走 PERP_FUNDING 分支（回归锚 #456）。
// SWAPARB + ExpectedSwapBpsDaily set + ExpectedNetBps=0 → 走 2b 分支（settleDays），不走 2a。
func TestEnrichOne_SwapARBRegressionNoPerpFundingBranch(t *testing.T) {
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "52993526:mt5", Symbol: "AUDJPYm", Pnl: -40.0, Swap: 4.99, PositionId: 456},
		{VenueKey: "277842155:mt5", Symbol: "AUDJPY.", Pnl: 5.0, Swap: 0, PositionId: 456},
	})
	s := newEnrichService(bpm) // store=nil → notional=0 → 2b 也缺省
	pb := &v1.Position{
		Id: 456, OpportunityId: "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5",
		Type: "SWAPARB", Status: "OPEN", ExpectedNetBps: 0, // SWAPARB 不用 expected_net_bps
		OpenedAt: timestamppb.New(time.Now().AddDate(0, 0, -3)),
	}
	swapBps := 0.494
	pb.ExpectedSwapBpsDaily = &swapBps
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "52993526:mt5", Symbol: "AUDJPYm", Status: "FILLED", Qty: 1.0, PriceActual: ptrFloat(113.517), Ts: time.Now().AddDate(0, 0, -3)},
		{Phase: "OPEN", Venue: "277842155:mt5", Symbol: "AUDJPY.", Status: "FILLED", Qty: 1.0, PriceActual: ptrFloat(113.503), Ts: time.Now().AddDate(0, 0, -3)},
	}
	now := time.Now()
	enrichOne(context.Background(), s, pb, legs, bpm.Snapshot(), now, map[string]time.Weekday{}, map[string]float64{}, map[int64]float64{})

	// 回归锚：floating/swapAccrued/net 与 #456 同值
	if pb.FloatingPnlUsd == nil || math.Abs(*pb.FloatingPnlUsd-(-35.0)) > 0.001 {
		t.Errorf("floating = %v, want -35.0", pb.FloatingPnlUsd)
	}
	if pb.SwapAccruedUsd == nil || math.Abs(*pb.SwapAccruedUsd-4.99) > 0.001 {
		t.Errorf("swap_accrued = %v, want 4.99", pb.SwapAccruedUsd)
	}
	if pb.NetPnlUsd == nil || math.Abs(*pb.NetPnlUsd-(-30.01)) > 0.001 {
		t.Errorf("net = %v, want -30.01", pb.NetPnlUsd)
	}
	// store=nil → notional=0 → 2b 也缺省（expected_swap_usd_to_date nil）
	if pb.ExpectedSwapUsdToDate != nil {
		t.Errorf("store=nil → expected_swap_usd_to_date 应 nil (2b also skips), got %v", *pb.ExpectedSwapUsdToDate)
	}
}

// TestEnrichOne_PerpFundingAttainmentFormula PERP_FUNDING 达成率公式验证（纯函数层）。
// expected=500, swapAccrued=125 → attainment=25%。
func TestEnrichOne_PerpFundingAttainmentFormula(t *testing.T) {
	expected := perpFundingExpectedUSD(5.0, 10000, 365) // = 500
	if math.Abs(expected-500.0) > 0.001 {
		t.Fatalf("expected = %.4f, want 500.0", expected)
	}
	swapAccrued := 125.0
	pct := swapAccrued / expected * 100
	if math.Abs(pct-25.0) > 0.001 {
		t.Errorf("attainment = %.4f%%, want 25.0%%", pct)
	}
}

// TestCarryTypes_XAssetCarry D-311 R7①：XASSET_CARRY 入息差型集合（§1 回本周期启用）。
func TestCarryTypes_XAssetCarry(t *testing.T) {
	if !carryTypes["XASSET_CARRY"] {
		t.Error("carryTypes 应含 XASSET_CARRY")
	}
}

// TestEnrichOne_XAssetCarryThreeCols D-311 R7②：XASSET_CARRY 走 PERP_FUNDING 同款年化模型——
// expectedNetBps=27.69、notional=EffectiveQty(10.12 基础币)×63.5=642.62、holdDays=3 →
// 应得=0.2769×642.62×3/365≈1.463；实收 funding=0.73 → 达成≈49.9%；§1 回本周期出值。
// 旧码类型门控只放 PERP_FUNDING → 三列恒 nil。
func TestEnrichOne_XAssetCarryThreeCols(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "gate:perp", Symbol: "CL_USDT", Pnl: -2.0, Swap: 0, PositionId: 484},
	})
	s := newEnrichService(bpm)
	s.store = st // notional 需 store 非 nil（ContractSizeByCanonical miss→cs=1）
	pb := &v1.Position{
		Id: 484, OpportunityId: "XASSET:CLUSDT:gate:perp:3001157:mt5",
		Type: "XASSET_CARRY", Status: "OPEN", ExpectedNetBps: 27.69,
		OpenedAt: timestamppb.New(time.Now().AddDate(0, 0, -3)),
	}
	filled := 10.12
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "gate:perp", Symbol: "CL_USDT", Status: "FILLED",
			Qty: 1012, FilledQty: &filled, PriceActual: ptrFloat(63.5), Ts: time.Now().AddDate(0, 0, -3)},
	}
	now := time.Now()
	enrichOne(context.Background(), s, pb, legs, bpm.Snapshot(), now, map[string]time.Weekday{}, map[string]float64{"484\x00gate:perp": 0.73}, map[int64]float64{})

	notional := 10.12 * 63.5 // 642.62（EffectiveQty 成交口径，勿按 qty=1012 合约数）
	wantExpected := 0.2769 * notional * 3.0 / 365
	if pb.ExpectedSwapUsdToDate == nil {
		t.Fatal("XASSET_CARRY 应得应出值（类型门控已放行），got nil")
	}
	if math.Abs(*pb.ExpectedSwapUsdToDate-wantExpected) > 0.01 {
		t.Errorf("expected=%.4f, want %.4f（27.69%%×%.2f×3/365）", *pb.ExpectedSwapUsdToDate, wantExpected, notional)
	}
	if pb.AttainmentPct == nil {
		t.Fatal("达成应出值（swapAccrued=0.73>0），got nil")
	}
	if want := 0.73 / wantExpected * 100; math.Abs(*pb.AttainmentPct-want) > 0.5 {
		t.Errorf("attainment=%.2f%%, want ≈%.2f%%", *pb.AttainmentPct, want)
	}
	// §1 回本周期（carryTypes 门控）：D-391 新口径——本=入场摩擦（本 fixture 无 fee/佣金/
	// 正点差，xassetCexFeeBps=0 不估算）→ 本=0，实收 0.73 已覆盖 → 已回本=0。
	if pb.BreakEvenDays == nil {
		t.Error("XASSET_CARRY 回本周期应出值（carryTypes 已含），got nil")
	} else if *pb.BreakEvenDays != 0 {
		t.Errorf("breakEven=%.2f, want 0（本=0 实收已覆盖）", *pb.BreakEvenDays)
	}
}

// TestEnrichOne_ExpectedBreakevenFallback D-311 R10：回本列预期值回落——
// opp.breakeven_days 透传 expected_breakeven_days（实测缺省时前端回落「预 X.X 天」）。
// 实测有值时 expected 仍透传（展示侧实测优先）；opp 无行/NULL/负值 → 缺省。
func TestEnrichOne_ExpectedBreakevenFallback(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	be, neg := 1.5, -0.5
	mustOpp := func(id string, breakeven *float64) {
		_, err := st.UpsertOpportunity(ctx, &pgstore.Opportunity{
			ID: id, Type: "XASSET_CARRY", Canonical: "CLUSDT",
			VenueBuy: "gate:perp", VenueSell: "3001157:mt5",
			PriceBuy: 63.5, PriceSell: 63.4, NetBps: 27.69, BreakevenDays: breakeven,
			QuoteTime: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("upsert opp %s: %v", id, err)
		}
	}
	mustOpp("XASSET:R10A:gate:perp:3001157:mt5", &be)
	mustOpp("XASSET:R10B:gate:perp:3001157:mt5", nil)  // breakeven NULL
	mustOpp("XASSET:R10C:gate:perp:3001157:mt5", &neg) // 负值 → 缺省

	bpm := fakeBPMWithCache(nil)
	s := newEnrichService(bpm)
	s.store = st
	now := time.Now()
	mkPos := func(id int64, oppID string) *v1.Position {
		return &v1.Position{Id: id, OpportunityId: oppID, Type: "XASSET_CARRY", Status: "OPEN",
			OpenedAt: timestamppb.New(now.AddDate(0, 0, -3))}
	}

	// ① 实测缺省（matched=false+funding=0 → breakEven nil）→ expected=1.5 透传
	pb1 := mkPos(901, "XASSET:R10A:gate:perp:3001157:mt5")
	enrichOne(ctx, s, pb1, nil, bpm.Snapshot(), now, map[string]time.Weekday{}, map[string]float64{}, map[int64]float64{})
	if pb1.BreakEvenDays != nil {
		t.Fatalf("case1 breakEven 应缺省（无收息），got %v", *pb1.BreakEvenDays)
	}
	if pb1.ExpectedBreakevenDays == nil || math.Abs(*pb1.ExpectedBreakevenDays-1.5) > 1e-9 {
		t.Fatalf("case1 expected=%v, want 1.5（opp.breakeven_days 透传）", pb1.ExpectedBreakevenDays)
	}

	// ② 实测有值（funding>0 → breakEven=0 已回本）→ expected 仍透传，前端优先实测。
	// D-322 S3b：funding 须经 OPEN CEX 腿归属——fixture 带 gate:perp 腿（未匹配 bp → cashflow 兜底计入）。
	pb2 := mkPos(902, "XASSET:R10A:gate:perp:3001157:mt5")
	legs902 := []pgstore.PositionLeg{{Phase: "OPEN", Venue: "gate:perp", Symbol: "CL_USDT", Status: "FILLED", Qty: 10.12}}
	enrichOne(ctx, s, pb2, legs902, bpm.Snapshot(), now, map[string]time.Weekday{}, map[string]float64{"902\x00gate:perp": 0.73}, map[int64]float64{})
	if pb2.BreakEvenDays == nil {
		t.Fatal("case2 breakEven 应出值（funding>0 实测），got nil")
	}
	if pb2.ExpectedBreakevenDays == nil || math.Abs(*pb2.ExpectedBreakevenDays-1.5) > 1e-9 {
		t.Fatalf("case2 expected=%v, want 1.5（实测有值仍透传，展示侧实测优先）", pb2.ExpectedBreakevenDays)
	}

	// ③ opp breakeven NULL → expected 缺省；④ 负值 → 缺省
	for _, tc := range []struct {
		id    int64
		oppID string
	}{{903, "XASSET:R10B:gate:perp:3001157:mt5"}, {904, "XASSET:R10C:gate:perp:3001157:mt5"}} {
		pb := mkPos(tc.id, tc.oppID)
		enrichOne(ctx, s, pb, nil, bpm.Snapshot(), now, map[string]time.Weekday{}, map[string]float64{}, map[int64]float64{})
		if pb.ExpectedBreakevenDays != nil {
			t.Errorf("opp %s expected 应缺省（NULL/负值），got %v", tc.oppID, *pb.ExpectedBreakevenDays)
		}
	}

	// ⑤ store=nil → 静默缺省不 panic
	s2 := newEnrichService(fakeBPMWithCache(nil))
	pb5 := mkPos(905, "XASSET:R10A:gate:perp:3001157:mt5")
	enrichOne(ctx, s2, pb5, nil, nil, now, map[string]time.Weekday{}, map[string]float64{}, map[int64]float64{})
	if pb5.ExpectedBreakevenDays != nil {
		t.Errorf("store=nil expected 应缺省，got %v", *pb5.ExpectedBreakevenDays)
	}
}
