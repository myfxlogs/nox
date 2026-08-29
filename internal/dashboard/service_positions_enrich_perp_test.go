package dashboard

// D-146 S5 微件②单测：PERP_FUNDING 三列（预期/实收/达成）计算 + SWAPARB 不变（回归锚 #456）。

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

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
	fundingByPos := map[int64]float64{555: 3.0}
	enrichOne(context.Background(), s, pb, legs, bpm.Snapshot(), now, map[string]time.Weekday{}, fundingByPos)

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
	enrichOne(context.Background(), s, pb, legs, bpm.Snapshot(), now, map[string]time.Weekday{}, map[int64]float64{})

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
