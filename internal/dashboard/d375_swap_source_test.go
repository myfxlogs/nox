// d375_swap_source_test.go —— D-375 S5：息差已收列换源锚（先红后绿）。
// 口径重定义：swapAccrued = broker 快照 Σ swap + income_events 实收（estimated cashflow 退出本列，
// 新列 SwapEstimatedUsd 承接参考值，>0 即填独立于 matched 门控）。
package dashboard

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// d375Pb 构造 XASSET_CARRY 持仓行（binance:perp 腿，494 实证形状）。
func d375Pb(id int64) *v1.Position {
	return &v1.Position{
		Id: id, OpportunityId: "XASSET_CARRY:SOLUSDT:277842155:mt5:binance:perp",
		Type: "XASSET_CARRY", Status: "OPEN",
		OpenedAt: timestamppb.New(time.Now().AddDate(0, 0, -3)),
	}
}

func d375BinanceLeg() pgstore.PositionLeg {
	return pgstore.PositionLeg{Phase: "OPEN", Venue: "binance:perp", Symbol: "SOLUSDT", Status: "FILLED", Qty: 1, Ts: time.Now().AddDate(0, 0, -3)}
}

func d375MtLeg() pgstore.PositionLeg {
	return pgstore.PositionLeg{Phase: "OPEN", Venue: "277842155:mt5", Symbol: "XAUUSD", Status: "FILLED", Qty: 1, Ts: time.Now().AddDate(0, 0, -3)}
}

// TestD375_Enrich_IncomeActualAndEstimated 锚3a：CEX 腿 income 实收入 swapAccruedUsd；
// estimated 只入 swapEstimatedUsd 不入 accrued（名不符实修复主锚，494 实证值）。
func TestD375_Enrich_IncomeActualAndEstimated(t *testing.T) {
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "binance:perp", Symbol: "SOLUSDT", Pnl: 1.0, Swap: 0, PositionId: 494},
	})
	s := newEnrichService(bpm)
	pb := d375Pb(494)
	income := map[string]float64{"494\x00binance:perp": 0.6458}
	est := map[int64]float64{494: 4.63}
	enrichOne(context.Background(), s, pb, []pgstore.PositionLeg{d375BinanceLeg()}, bpm.Snapshot(), time.Now(), map[string]time.Weekday{}, income, est)

	if pb.SwapAccruedUsd == nil || math.Abs(*pb.SwapAccruedUsd-0.6458) > 1e-9 {
		t.Fatalf("锚3a: swapAccruedUsd = %v, want 0.6458（income 实收；红=仍在用 estimated cashflow）", pb.SwapAccruedUsd)
	}
	if pb.SwapEstimatedUsd == nil || math.Abs(*pb.SwapEstimatedUsd-4.63) > 1e-9 {
		t.Fatalf("锚3a: swapEstimatedUsd = %v, want 4.63", pb.SwapEstimatedUsd)
	}
	if pb.NetPnlUsd == nil || math.Abs(*pb.NetPnlUsd-1.6458) > 1e-9 {
		t.Fatalf("锚3a: netPnlUsd = %v, want 1+0.6458（估算不入净值）", pb.NetPnlUsd)
	}
}

// TestD375_Enrich_VenueReportedNoDoubleCount 锚3b：bp.Swap≠0（venue 实报）的 venue 跳过 income，防双计。
func TestD375_Enrich_VenueReportedNoDoubleCount(t *testing.T) {
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "gate:perp", Symbol: "SOLUSDT", Pnl: 1.0, Swap: 0.5, PositionId: 495},
	})
	s := newEnrichService(bpm)
	pb := d375Pb(495)
	gateLeg := d375BinanceLeg()
	gateLeg.Venue = "gate:perp" // venue 与 bp 对齐（gate 实报腿）
	income := map[string]float64{"495\x00gate:perp": 0.73}
	enrichOne(context.Background(), s, pb, []pgstore.PositionLeg{gateLeg}, bpm.Snapshot(), time.Now(), map[string]time.Weekday{}, income, map[int64]float64{})

	if pb.SwapAccruedUsd == nil || math.Abs(*pb.SwapAccruedUsd-0.5) > 1e-9 {
		t.Fatalf("锚3b: swapAccruedUsd = %v, want 0.5（venue 实报优先，income 不双计）", pb.SwapAccruedUsd)
	}
}

// TestD375_Enrich_EstimatedIndependentOfGate 锚3c：估行独立于 matched||funding 门控——
// 未匹配+零实收 → accrued 缺省「—」但 SwapEstimatedUsd 仍出（参考行不因实收缺失隐藏）。
func TestD375_Enrich_EstimatedIndependentOfGate(t *testing.T) {
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "binance:perp", Symbol: "SOLUSDT", Pnl: 1.0, Swap: 0, PositionId: 999}, // 位置不匹配
	})
	s := newEnrichService(bpm)
	pb := d375Pb(494)
	est := map[int64]float64{494: 4.63}
	enrichOne(context.Background(), s, pb, []pgstore.PositionLeg{d375BinanceLeg()}, bpm.Snapshot(), time.Now(), map[string]time.Weekday{}, map[string]float64{}, est)

	if pb.SwapAccruedUsd != nil {
		t.Fatalf("锚3c: 未匹配+零实收 → swapAccruedUsd 应缺省, got %v", *pb.SwapAccruedUsd)
	}
	if pb.SwapEstimatedUsd == nil || math.Abs(*pb.SwapEstimatedUsd-4.63) > 1e-9 {
		t.Fatalf("锚3c: swapEstimatedUsd = %v, want 4.63（独立于 matched 门控）", pb.SwapEstimatedUsd)
	}
}

// TestD375_Enrich_PureMtUnchanged 锚4：纯 MT 仓行为不变（broker swap 唯一源，零 income/estimated）。
func TestD375_Enrich_PureMtUnchanged(t *testing.T) {
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "277842155:mt5", Symbol: "XAUUSD", Pnl: 2.0, Swap: 0.8, PositionId: 496},
	})
	s := newEnrichService(bpm)
	pb := d375Pb(496)
	enrichOne(context.Background(), s, pb, []pgstore.PositionLeg{d375MtLeg()}, bpm.Snapshot(), time.Now(), map[string]time.Weekday{}, map[string]float64{}, map[int64]float64{})

	if pb.SwapAccruedUsd == nil || math.Abs(*pb.SwapAccruedUsd-0.8) > 1e-9 {
		t.Fatalf("锚4: 纯 MT 仓 swapAccruedUsd = %v, want 0.8（broker 快照唯一源）", pb.SwapAccruedUsd)
	}
	if pb.SwapEstimatedUsd != nil {
		t.Fatalf("锚4: 纯 MT 仓零 estimated → swapEstimatedUsd 应缺省, got %v", *pb.SwapEstimatedUsd)
	}
}

// TestD375_Enrich_MixedXAssetComposition 锚6：混合 XASSET——MT 腿 broker swap + binance 腿 income 同列合成。
func TestD375_Enrich_MixedXAssetComposition(t *testing.T) {
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "277842155:mt5", Symbol: "XAUUSD", Pnl: 2.0, Swap: 0.8, PositionId: 497},
		{VenueKey: "binance:perp", Symbol: "SOLUSDT", Pnl: 1.0, Swap: 0, PositionId: 497},
	})
	s := newEnrichService(bpm)
	pb := d375Pb(497)
	income := map[string]float64{"497\x00binance:perp": 0.6458}
	enrichOne(context.Background(), s, pb, []pgstore.PositionLeg{d375MtLeg(), d375BinanceLeg()}, bpm.Snapshot(), time.Now(), map[string]time.Weekday{}, income, map[int64]float64{})

	if pb.SwapAccruedUsd == nil || math.Abs(*pb.SwapAccruedUsd-1.4458) > 1e-9 {
		t.Fatalf("锚6: swapAccruedUsd = %v, want 0.8+0.6458（MT broker+CEX income 同列）", pb.SwapAccruedUsd)
	}
}

// TestD375_AttainmentUsesIncome 锚3d：attainment 分子=实收——真库 notional 合成
// （computeEntryNotional：两 FILLED 腿 50000×0.2=2×10000=20000；ExpectedNetBps=10%×1y → 应得 2000，
// income 实收 100 → 达成 5%）。
func TestD375_AttainmentUsesIncome(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_SERVICE_DSN")
	s := &Service{store: st}
	pb := &v1.Position{
		Id: 600, OpportunityId: "XASSET_CARRY:BTCUSDT:277842155:mt5:binance:perp",
		Type: "XASSET_CARRY", Status: "OPEN", ExpectedNetBps: 10.0,
		OpenedAt: timestamppb.New(time.Now().AddDate(0, 0, -365)),
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "277842155:mt5", Symbol: "BTCUSD", Status: "FILLED", Qty: 0.2, PriceActual: ptrFloat(50000), Ts: time.Now().AddDate(0, 0, -365)},
		{Phase: "OPEN", Venue: "binance:perp", Symbol: "BTCUSDT", Status: "FILLED", Qty: 0.2, PriceActual: ptrFloat(50000), Ts: time.Now().AddDate(0, 0, -365)},
	}
	income := map[string]float64{"600\x00binance:perp": 100.0}
	enrichOne(context.Background(), s, pb, legs, nil, time.Now(), map[string]time.Weekday{}, income, map[int64]float64{})

	if pb.ExpectedSwapUsdToDate == nil || math.Abs(*pb.ExpectedSwapUsdToDate-2000) > 0.01 {
		t.Fatalf("锚3d: expected = %v, want 2000（20000 名义×10%% 年化×满年）", pb.ExpectedSwapUsdToDate)
	}
	if pb.AttainmentPct == nil || math.Abs(*pb.AttainmentPct-5.0) > 0.01 {
		t.Fatalf("锚3d: attainment = %v, want 5.0（分子=income 实收 100）", pb.AttainmentPct)
	}
}

// TestD375_AnnotateCEXSwap_IncomeSource 锚5：annotateCEXSwap bp.Swap=0 腿兜底源=income 实收
// 非 estimated cashflow；venue 实报（≠0）真值优先不动。
func TestD375_AnnotateCEXSwap_IncomeSource(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx,
		`TRUNCATE income_events, positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var posID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('XASSET_CARRY:SOLUSDT:a:b','XASSET_CARRY','OPEN',0) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
		 VALUES ($1,1,'OPEN','binance','perp','SOLUSDT','BUY',1,100,'FILLED')`, posID); err != nil {
		t.Fatalf("seed leg: %v", err)
	}
	one := int16(1)
	for _, tc := range []struct {
		amount float64
		tran   int64
	}{{0.5, 201}, {0.6458, 202}} {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, simulated)
			 VALUES ('binance','perp','t','k','s',false) ON CONFLICT DO NOTHING`); err != nil {
			t.Fatalf("seed config: %v", err)
		}
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO income_events (venue, venue_type, exchange_config_id, symbol, income_type, amount_usd, tran_id, ts, position_id, leg_no)
			 VALUES ('binance','perp',(SELECT id FROM exchange_configs WHERE venue='binance' AND venue_type='perp' AND NOT simulated),'SOLUSDT','FUNDING_FEE',$1,$2,now(),$3,$4)`,
			tc.amount, tc.tran, &posID, &one); err != nil {
			t.Fatalf("seed income: %v", err)
		}
	}
	bps := []*v1.BrokerPosition{
		{VenueKey: "binance:perp", Symbol: "SOLUSDT", Swap: 0, PositionId: posID}, // 兜底腿
		{VenueKey: "gate:perp", Symbol: "SOLUSDT", Swap: 2.5, PositionId: posID},  // 实报腿
	}
	annotateCEXSwap(ctx, st, bps)
	if math.Abs(bps[0].Swap-1.1458) > 1e-9 {
		t.Fatalf("锚5: binance 腿 swap = %v, want 1.1458（income 实收兜底；红=仍用 estimated cashflow）", bps[0].Swap)
	}
	if bps[1].Swap != 2.5 {
		t.Fatalf("锚5: gate 腿实报 swap 被覆写: %v, want 2.5", bps[1].Swap)
	}
}
