package dashboard

// D-146 S5 回本口径扩展单测：CEX 仓 funding 实收并入 swapAccrued + net 同口径。
// 三场景：①CEX 仓有 funding 流→回本显示数值 ②纯 MT 仓行为不变（回归锚 #456=TestEnrichPositions_MatchedSum）
//         ③零流仓（matched=false + funding=0）仍「—」。

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestEnrichOne_CexFundingMerged CEX 仓（PERP_FUNDING）matched=true + funding 流 → swapAccrued 含 funding，net 同口径。
// broker 快照 swap=0（CEX 不报 swap），funding=3.0（position_cashflows 实收）→ swapAccrued=3.0，net=−10+0+3=−7。
func TestEnrichOne_CexFundingMerged(t *testing.T) {
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "binance:spot", Symbol: "BTCUSDT", Pnl: -10.0, Swap: 0, Commission: 0, PositionId: 456},
	})
	s := newEnrichService(bpm) // store=nil → notional 跳过
	pb := &v1.Position{
		Id: 456, OpportunityId: "PERP_FUNDING:BTCUSDT:binance:spot:okx:perp",
		Type: "PERP_FUNDING", Status: "OPEN",
		OpenedAt: timestamppb.New(time.Now().AddDate(0, 0, -3)),
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "binance:spot", Symbol: "BTCUSDT", Status: "FILLED", Qty: 0.01, PriceActual: ptrFloat(60000), Ts: time.Now().AddDate(0, 0, -3)},
	}
	now := time.Now()
	fundingByPos := map[string]float64{"456\x00binance:spot": 3.0} // CEX funding 实收（venue 未报 swap=0 → cashflow 兜底计入）
	enrichOne(context.Background(), s, pb, legs, bpm.Snapshot(), now, map[string]time.Weekday{}, fundingByPos)

	// swapAccrued = swap 快照(0) + funding(3.0) = 3.0
	if pb.SwapAccruedUsd == nil {
		t.Fatal("swap_accrued should not be nil (CEX funding merged)")
	}
	if math.Abs(*pb.SwapAccruedUsd-3.0) > 0.001 {
		t.Errorf("swap_accrued = %.4f, want 3.0 (swap=0+funding=3)", *pb.SwapAccruedUsd)
	}
	// net = floating(-10) + commission(0) + swapAccrued(3.0) = -7.0
	if pb.NetPnlUsd == nil {
		t.Fatal("net should not be nil")
	}
	if math.Abs(*pb.NetPnlUsd-(-7.0)) > 0.001 {
		t.Errorf("net = %.4f, want -7.0 (floating=-10+swap+funding=3)", *pb.NetPnlUsd)
	}
	// 回本周期 = |net| ÷ (swapAccrued/holdDays) = 7.0 ÷ (3.0/3) = 7.0
	if pb.BreakEvenDays == nil {
		t.Fatal("break_even_days should not be nil (PERP_FUNDING + funding>0)")
	}
	wantBE := 7.0 / (3.0 / 3.0)
	if math.Abs(*pb.BreakEvenDays-wantBE) > 0.01 {
		t.Errorf("break_even_days = %.4f, want %.4f", *pb.BreakEvenDays, wantBE)
	}
}

// TestEnrichOne_CexFundingNoMatchButFunding CEX 仓 matched=false（无 broker 快照）但有 funding 流 → 仍出值。
// 零 broker 快照 → floating=0, swap=0, commission=0；funding=2.0 → swapAccrued=2.0, net=2.0。
func TestEnrichOne_CexFundingNoMatchButFunding(t *testing.T) {
	bpm := fakeBPMWithCache(nil) // 无 broker 快照
	s := newEnrichService(bpm)
	pb := &v1.Position{
		Id: 789, OpportunityId: "PERP_FUNDING:ETHUSDT:binance:spot:okx:perp",
		Type: "PERP_FUNDING", Status: "OPEN",
		OpenedAt: timestamppb.New(time.Now().AddDate(0, 0, -2)),
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "binance:spot", Symbol: "ETHUSDT", Status: "FILLED", Qty: 0.1, PriceActual: ptrFloat(2500), Ts: time.Now().AddDate(0, 0, -2)},
	}
	now := time.Now()
	fundingByPos := map[string]float64{"789\x00binance:spot": 2.0}
	enrichOne(context.Background(), s, pb, legs, bpm.Snapshot(), now, map[string]time.Weekday{}, fundingByPos)

	// matched=false 但 funding>0 → 仍出值
	if pb.SwapAccruedUsd == nil {
		t.Fatal("swap_accrued should not be nil (funding>0 even if no broker match)")
	}
	if math.Abs(*pb.SwapAccruedUsd-2.0) > 0.001 {
		t.Errorf("swap_accrued = %.4f, want 2.0 (funding only)", *pb.SwapAccruedUsd)
	}
	if pb.NetPnlUsd == nil {
		t.Fatal("net should not be nil")
	}
	if math.Abs(*pb.NetPnlUsd-2.0) > 0.001 {
		t.Errorf("net = %.4f, want 2.0 (funding only, no floating/commission)", *pb.NetPnlUsd)
	}
	// 回本周期=0（net=2.0>0 → 已回本）
	if pb.BreakEvenDays == nil {
		t.Fatal("break_even_days should not be nil")
	}
	if *pb.BreakEvenDays != 0 {
		t.Errorf("net>0 break_even_days want 0 (已回本), got %v", *pb.BreakEvenDays)
	}
}

// TestEnrichOne_ZeroFlowStillDash 零流仓（matched=false + funding=0）→ 三字段缺省（仍「—」）。
func TestEnrichOne_ZeroFlowStillDash(t *testing.T) {
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "other:mt5", Symbol: "EURUSDm", Pnl: 10, Swap: 1, PositionId: 999}, // 不匹配
	})
	s := newEnrichService(bpm)
	pb := &v1.Position{
		Id: 999, Type: "PERP_FUNDING", Status: "OPEN",
		OpenedAt: timestamppb.New(time.Now().AddDate(0, 0, -1)),
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "binance:spot", Symbol: "BTCUSDT", Status: "FILLED", Qty: 0.01, PriceActual: ptrFloat(60000), Ts: time.Now()},
	}
	now := time.Now()
	enrichOne(context.Background(), s, pb, legs, bpm.Snapshot(), now, map[string]time.Weekday{}, map[string]float64{})

	if pb.FloatingPnlUsd != nil || pb.SwapAccruedUsd != nil || pb.NetPnlUsd != nil {
		t.Errorf("零流仓应全 nil: floating=%v swap=%v net=%v", pb.FloatingPnlUsd, pb.SwapAccruedUsd, pb.NetPnlUsd)
	}
	if pb.BreakEvenDays != nil {
		t.Errorf("零流仓 break_even_days 应 nil, got %v", *pb.BreakEvenDays)
	}
}

// TestEnrichOne_PureMTRegression 纯 MT 仓（SWAPARB）无 funding → 行为不变（回归锚 #456）。
// fundingByPos 空 map → funding=0 → swapAccrued=swap 快照，net=floating+swap+commission（原口径）。
func TestEnrichOne_PureMTRegression(t *testing.T) {
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "52993526:mt5", Symbol: "AUDJPYm", Pnl: -40.0, Swap: 4.99, PositionId: 456},
		{VenueKey: "277842155:mt5", Symbol: "AUDJPY.", Pnl: 5.0, Swap: 0, PositionId: 456},
	})
	s := newEnrichService(bpm)
	pb := &v1.Position{
		Id: 456, OpportunityId: "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5",
		Type: "SWAPARB", Status: "OPEN",
		OpenedAt: timestamppb.New(time.Now().AddDate(0, 0, -3)),
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "52993526:mt5", Symbol: "AUDJPYm", Status: "FILLED", Qty: 1.0, PriceActual: ptrFloat(113.517), Ts: time.Now().AddDate(0, 0, -3)},
		{Phase: "OPEN", Venue: "277842155:mt5", Symbol: "AUDJPY.", Status: "FILLED", Qty: 1.0, PriceActual: ptrFloat(113.503), Ts: time.Now().AddDate(0, 0, -3)},
	}
	now := time.Now()
	// 空 fundingByPos → 纯 MT 仓无 funding 加成
	enrichOne(context.Background(), s, pb, legs, bpm.Snapshot(), now, map[string]time.Weekday{}, map[string]float64{})

	// 回归锚：与 TestEnrichPositions_MatchedSum 同值（funding=0 不改变口径）
	if pb.FloatingPnlUsd == nil || math.Abs(*pb.FloatingPnlUsd-(-35.0)) > 0.001 {
		t.Errorf("floating = %v, want -35.0", pb.FloatingPnlUsd)
	}
	if pb.SwapAccruedUsd == nil || math.Abs(*pb.SwapAccruedUsd-4.99) > 0.001 {
		t.Errorf("swap_accrued = %v, want 4.99 (无 funding 加成)", pb.SwapAccruedUsd)
	}
	if pb.NetPnlUsd == nil || math.Abs(*pb.NetPnlUsd-(-30.01)) > 0.001 {
		t.Errorf("net = %v, want -30.01", pb.NetPnlUsd)
	}
	wantBE := 30.01 / (4.99 / 3.0)
	if pb.BreakEvenDays == nil || math.Abs(*pb.BreakEvenDays-wantBE) > 0.01 {
		t.Errorf("break_even_days = %v, want %.4f", pb.BreakEvenDays, wantBE)
	}
}

// TestEnrichOne_VenueActualNotDoubleCounted D-322 S3b：venue 真值优先去重——
// CEX 腿匹配到 broker position 且 bp.Swap=1.5（venue 实报，matchBrokerLegs 已计入 swapAccrued）
// → 该 venue 的 cashflow 估算（2.5）跳过，swapAccrued=1.5 而非 4.0；
// 旧码（D-146）两源相加=同腿 actual+estimate 双计（pos 485：+0.12 实收 + 估算异号相加双重污染）。
func TestEnrichOne_VenueActualNotDoubleCounted(t *testing.T) {
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "okx:perp", Symbol: "BTCUSDT", Pnl: -8.0, Swap: 1.5, Commission: -0.5, PositionId: 321},
	})
	s := newEnrichService(bpm)
	pb := &v1.Position{
		Id: 321, OpportunityId: "PERP_FUNDING:BTCUSDT:okx:perp:binance:spot",
		Type: "PERP_FUNDING", Status: "OPEN",
		OpenedAt: timestamppb.New(time.Now().AddDate(0, 0, -2)),
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "okx:perp", Symbol: "BTCUSDT", Status: "FILLED", Qty: 0.01, PriceActual: ptrFloat(60000), Ts: time.Now().AddDate(0, 0, -2)},
	}
	now := time.Now()
	fundingByPos := map[string]float64{"321\x00okx:perp": 2.5}
	enrichOne(context.Background(), s, pb, legs, bpm.Snapshot(), now, map[string]time.Weekday{}, fundingByPos)

	// swapAccrued = venue 实报 1.5（cashflow 估算 2.5 去重跳过，不双计）
	if pb.SwapAccruedUsd == nil || math.Abs(*pb.SwapAccruedUsd-1.5) > 0.001 {
		t.Errorf("swap_accrued = %v, want 1.5 (venue 实报优先，估算不双计)", pb.SwapAccruedUsd)
	}
	// net = floating(-8) + commission(-0.5) + swapAccrued(1.5) = -7.0
	if pb.NetPnlUsd == nil || math.Abs(*pb.NetPnlUsd-(-7.0)) > 0.001 {
		t.Errorf("net = %v, want -7.0", pb.NetPnlUsd)
	}
	// 回本 = |net| ÷ (swapAccrued/holdDays) = 7.0 ÷ (1.5/2) ≈ 9.33
	wantBE := 7.0 / (1.5 / 2.0)
	if pb.BreakEvenDays == nil || math.Abs(*pb.BreakEvenDays-wantBE) > 0.01 {
		t.Errorf("break_even_days = %v, want %.4f", pb.BreakEvenDays, wantBE)
	}
}
