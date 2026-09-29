package dashboard

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// TestResolveLegs 验证 resolveLegs 从 qbus 取双腿 broker 原生符号（D-051 修②）：
// VenueSymbol 正确、Side/Key/Qty 正确、双腿齐备。
func TestResolveLegs(t *testing.T) {
	b := bus.New()
	now := time.Now()
	b.Publish(bus.Quote{Symbol: "EURUSD", Venue: "b", VenueType: "mt5", Bid: 1.0, Ask: 1.0001, Time: now, VenueSymbol: "EURUSDm"})
	b.Publish(bus.Quote{Symbol: "EURUSD", Venue: "s", VenueType: "mt5", Bid: 1.0002, Ask: 1.0003, Time: now, VenueSymbol: "EURUSDm"})

	s := &Service{qbus: b, legContractSize: func(_, sym string) (float64, bool) { return 100000, true }}
	found := &pgstore.Opportunity{Canonical: "EURUSD", VenueBuy: "b:mt5", VenueSell: "s:mt5"}

	legs, err := s.resolveLegs(found, 0.1)
	if err != nil {
		t.Fatalf("resolveLegs unexpected error: %v", err)
	}
	if len(legs) != 2 {
		t.Fatalf("want 2 legs, got %d", len(legs))
	}

	buy, sell := legs[0], legs[1]
	if buy.VenueSymbol != "EURUSDm" || sell.VenueSymbol != "EURUSDm" {
		t.Errorf("VenueSymbol want EURUSDm for both, got %q / %q", buy.VenueSymbol, sell.VenueSymbol)
	}
	if buy.VenueKey != "b:mt5" || sell.VenueKey != "s:mt5" {
		t.Errorf("VenueKey want b:mt5 / s:mt5, got %q / %q", buy.VenueKey, sell.VenueKey)
	}
	if buy.Side != "BUY" || sell.Side != "SELL" {
		t.Errorf("Side want BUY / SELL, got %q / %q", buy.Side, sell.Side)
	}
	if buy.Qty != 0.1 || sell.Qty != 0.1 {
		t.Errorf("Qty want 0.1 / 0.1, got %v / %v", buy.Qty, sell.Qty)
	}
	if buy.PriceExpected != 1.0001 || sell.PriceExpected != 1.0002 {
		t.Errorf("PriceExpected want 1.0001 / 1.0002, got %v / %v", buy.PriceExpected, sell.PriceExpected)
	}
}

// TestResolveLegsNotionalScaling D-060 实施修正②：不等 CS（油 1000:100 桶/手）卖腿等名义反算
// 10×；CS 缺失拒；未接线拒。D-329：取整致失衡改两段式——pass-2 粗腿锚定匹配放行，
// 匹配仍超才拒（腿粒度无法执行，锚=d329_legs_match_test.go T3）。
func TestResolveLegsNotionalScaling(t *testing.T) {
	b := bus.New()
	now := time.Now()
	b.Publish(bus.Quote{Symbol: "BRENT", Venue: "b", VenueType: "mt5", Bid: 92.0, Ask: 92.1, Time: now, VenueSymbol: "UKOILm"})
	b.Publish(bus.Quote{Symbol: "BRENT", Venue: "s", VenueType: "mt5", Bid: 95.1, Ask: 95.2, Time: now, VenueSymbol: "XBRUSD"})
	found := &pgstore.Opportunity{Canonical: "BRENT", VenueBuy: "b:mt5", VenueSell: "s:mt5"}

	cs := map[string]float64{"UKOILm": 1000, "XBRUSD": 100}
	s := &Service{qbus: b, legContractSize: func(_, sym string) (float64, bool) { v, ok := cs[sym]; return v, ok }}

	// 3.3% 跨 venue 价差（套利缺口）不得误判为不平衡（共同参考价口径修正）。
	legs, err := s.resolveLegs(found, 0.1)
	if err != nil {
		t.Fatalf("oil pair with 3.3%% venue gap: %v", err)
	}
	if legs[1].Qty != 1.0 {
		t.Errorf("sell qty want 1.0 (0.1×1000/100), got %v", legs[1].Qty)
	}

	// CS 缺失 → 拒。
	s.legContractSize = func(_, _ string) (float64, bool) { return 0, false }
	if _, err := s.resolveLegs(found, 0.1); err == nil || !strings.Contains(err.Error(), "合约大小未知") {
		t.Errorf("missing CS want reject, got %v", err)
	}

	// 未接线 → 拒。
	s.legContractSize = nil
	if _, err := s.resolveLegs(found, 0.1); err == nil || !strings.Contains(err.Error(), "解析器未接线") {
		t.Errorf("解析器未接线 want reject, got %v", err)
	}

	// 取整致失衡 >2%（CS 1:6，qty 0.1 → sell 0.02，名义差 20%）→ D-329 pass-2：
	// 粗腿（sell cs=6，unit $6 vs buy $1）锚定 0.02 不动，buy 回算 0.1→0.12（=0.02×6）平衡放行。
	b2 := bus.New()
	b2.Publish(bus.Quote{Symbol: "X", Venue: "b", VenueType: "mt5", Bid: 100, Ask: 100, Time: now, VenueSymbol: "A"})
	b2.Publish(bus.Quote{Symbol: "X", Venue: "s", VenueType: "mt5", Bid: 100, Ask: 100, Time: now, VenueSymbol: "B"})
	cs2 := map[string]float64{"A": 1, "B": 6}
	s2 := &Service{qbus: b2, legContractSize: func(_, sym string) (float64, bool) { v, ok := cs2[sym]; return v, ok }}
	f2 := &pgstore.Opportunity{Canonical: "X", VenueBuy: "b:mt5", VenueSell: "s:mt5"}
	legs4, err := s2.resolveLegs(f2, 0.1)
	if err != nil {
		t.Errorf("rounding imbalance should match via pass-2 coarse anchor, got %v", err)
	} else if legs4[0].Qty != 0.12 || legs4[1].Qty != 0.02 {
		t.Errorf("matched qty want buy 0.12 / sell 0.02 (anchor), got %v / %v", legs4[0].Qty, legs4[1].Qty)
	}
}

// TestResolveLegsNoQuote 缺任一腿报价 → 返回错误（执行方 FAILED 透传）。
func TestResolveLegsNoQuote(t *testing.T) {
	b := bus.New()
	now := time.Now()
	// 只播买腿，卖腿无报价。
	b.Publish(bus.Quote{Symbol: "EURUSD", Venue: "b", VenueType: "mt5", Bid: 1.0, Ask: 1.0001, Time: now, VenueSymbol: "EURUSDm"})

	s := &Service{qbus: b}
	found := &pgstore.Opportunity{Canonical: "EURUSD", VenueBuy: "b:mt5", VenueSell: "s:mt5"}

	if _, err := s.resolveLegs(found, 0.1); err == nil {
		t.Fatal("missing sell leg want error, got nil")
	}
}

// TestCheckTradability D-060 F5 执行预检：禁交易腿拒绝（错误含 venue/符号/mode）、
// 未知放行、CEX 腿跳过、nil 未接线放行。
func TestCheckTradability(t *testing.T) {
	found := &pgstore.Opportunity{Canonical: "BTCUSDT", VenueBuy: "ic:mt5", VenueSell: "pxbt:mt5"}

	// 买腿 Full、卖腿 Disabled（PXBT 实证形态）→ 拒绝，错误含 venue+符号+mode。
	s := &Service{tradeModes: func(canonical, venueKey string) (int64, bool) {
		if venueKey == "pxbt:mt5" {
			return fact.TradeModeDisabled, true
		}
		return fact.TradeModeFull, true
	}}
	err := s.checkTradability(found)
	if err == nil {
		t.Fatal("禁交易卖腿应拒绝")
	}
	for _, want := range []string{"pxbt:mt5", "BTCUSDT", "0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息缺 %q: %v", want, err)
		}
	}

	// 卖腿 ShortOnly（可开空）→ 放行。
	s.tradeModes = func(_, vk string) (int64, bool) {
		if vk == "pxbt:mt5" {
			return fact.TradeModeShortOnly, true
		}
		return fact.TradeModeFull, true
	}
	if err := s.checkTradability(found); err != nil {
		t.Fatalf("卖腿 ShortOnly 应放行, got %v", err)
	}

	// 未知（TTL 过期/无 Fact）→ 放行。
	s.tradeModes = func(_, _ string) (int64, bool) { return 0, false }
	if err := s.checkTradability(found); err != nil {
		t.Fatalf("未知应放行, got %v", err)
	}

	// CEX 腿跳过（trade_mode 仅 MT 语义）：即便返回 Disabled 也不拒。
	cex := &pgstore.Opportunity{Canonical: "BTCUSDT", VenueBuy: "binance:spot", VenueSell: "okx:perp"}
	s.tradeModes = func(_, _ string) (int64, bool) { return fact.TradeModeDisabled, true }
	if err := s.checkTradability(cex); err != nil {
		t.Fatalf("CEX 腿应跳过预检, got %v", err)
	}

	// nil = 未接线 → 放行。
	s.tradeModes = nil
	if err := s.checkTradability(found); err != nil {
		t.Fatalf("未接线应放行, got %v", err)
	}
}

// TestNetUSD：净收益 bps → USD（D-053）。
// 价差型=一次性 USD（bps/10000×notional）；息差型=年化 USD/年（pct/100×notional）；notional≤0 不换算。
func TestNetUSD(t *testing.T) {
	cases := []struct {
		name     string
		netBps   float64
		typ      string
		notional float64
		want     float64
	}{
		{"cross one-shot", 48.2, "CROSS_VENUE", 1000, 4.82},
		{"cross zero bps", 0, "CROSS_VENUE", 1000, 0},
		{"carry annualized", 5.2, "CARRY", 1000, 52.0},
		{"funding annualized", 12.0, "FUNDING_HEDGE", 1000, 120.0},
		{"no notional", 48.2, "CROSS_VENUE", 0, 0},
		{"negative bps", -2, "CROSS_VENUE", 1000, -0.2},
	}
	for _, c := range cases {
		got := netUSD(c.netBps, c.typ, c.notional)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: netUSD(%v,%q,%v) want %v, got %v", c.name, c.netBps, c.typ, c.notional, c.want, got)
		}
	}
}

// TestResolveLegsVolumePreflight D-061 量规格预检：qty 低于 venue 最小手数 → 拒（买/卖腿同检）；
// 未知（ok=false）放行；CEX 腿跳过。
func TestResolveLegsVolumePreflight(t *testing.T) {
	b := bus.New()
	now := time.Now()
	b.Publish(bus.Quote{Symbol: "BRENT", Venue: "b", VenueType: "mt5", Bid: 95, Ask: 95, Time: now, VenueSymbol: "UKOILm"})
	b.Publish(bus.Quote{Symbol: "BRENT", Venue: "s", VenueType: "mt5", Bid: 95, Ask: 95, Time: now, VenueSymbol: "BRENT"})
	found := &pgstore.Opportunity{Canonical: "BRENT", VenueBuy: "b:mt5", VenueSell: "s:mt5"}

	cs := map[string]float64{"UKOILm": 1000, "BRENT": 1000}
	s := &Service{qbus: b,
		legContractSize: func(_, sym string) (float64, bool) { v, ok := cs[sym]; return v, ok },
		legMinLots: func(_, sym string) (float64, bool) {
			if sym == "BRENT" {
				return 1.0, true
			}
			return 0.01, true
		},
	}
	// qty 0.05：卖腿 BRENT min=1.0 → 拒。
	if _, err := s.resolveLegs(found, 0.05); err == nil || !strings.Contains(err.Error(), "低于 venue 最小手数") {
		t.Errorf("sell below min want reject, got %v", err)
	}
	// qty 1.0：双腿合法 → 过。
	if _, err := s.resolveLegs(found, 1.0); err != nil {
		t.Errorf("qty 1.0 want pass, got %v", err)
	}
	// 未知（ok=false）→ 放行。
	s.legMinLots = func(_, _ string) (float64, bool) { return 0, false }
	if _, err := s.resolveLegs(found, 0.05); err != nil {
		t.Errorf("unknown min want pass, got %v", err)
	}
}

// TestOppToProtoUSDDerivation D-062 显示层 USD 派生：
//   - 价差型（CROSS_VENUE）：gap/net/cost per-lot 公式值正确；
//   - 息差型（CARRY）：net_usd_per_lot_day 公式值正确，net_usd_per_lot 不填；
//   - CS 缺失 → 全部 USD per-lot 字段 0（前端回退 bps）；gap_usd_per_unit 始终填。
func TestOppToProtoUSDDerivation(t *testing.T) {
	const eps = 1e-6
	cs := 1000.0 // 桶/手
	resolver := func(canonical, venueKey string) (float64, bool) { return cs, true }

	// 价差型：BRENT buy 92.00 / sell 95.00，net 300 bps，cost 50 bps。
	//   gap = 3.00 $/桶；net/手 = 300/1e4 × 1000 × 92 = 2760；cost/手 = 50/1e4 × 1000 × 92 = 460。
	cross := &pgstore.Opportunity{
		Type: "CROSS_VENUE", Canonical: "BRENT", VenueBuy: "ic:mt5", VenueSell: "pxbt:mt5",
		PriceBuy: 92.0, PriceSell: 95.0, NetBps: 300,
		SpreadCost: 20, CommissionCost: 10, SlippageCost: 10, SwapCost: 10, // total 50
	}
	s := &Service{oppContractSize: resolver}
	pb := s.oppToProto(context.Background(), cross)
	if math.Abs(pb.GapUsdPerUnit-3.0) > eps {
		t.Errorf("cross gap_usd_per_unit want 3.0, got %v", pb.GapUsdPerUnit)
	}
	if math.Abs(pb.NetUsdPerLot-2760.0) > eps {
		t.Errorf("cross net_usd_per_lot want 2760, got %v", pb.NetUsdPerLot)
	}
	if math.Abs(pb.CostUsdPerLot-460.0) > eps {
		t.Errorf("cross cost_usd_per_lot want 460, got %v", pb.CostUsdPerLot)
	}
	if pb.NetUsdPerLotDay != 0 {
		t.Errorf("cross net_usd_per_lot_day want 0, got %v", pb.NetUsdPerLotDay)
	}

	// 息差型：TRX funding net 1095 bps（年化百分点），buy 0.10。
	//   net/日/手 = 1095/100 × 1000 × 0.10 / 365 = 3.0；cost/手 = 50/1e4 × 1000 × 0.10 = 0.5。
	carry := &pgstore.Opportunity{
		Type: "CARRY", Canonical: "TRXUSDT", VenueBuy: "binance:spot", VenueSell: "okx:perp",
		PriceBuy: 0.10, PriceSell: 0.1001, NetBps: 1095,
		SpreadCost: 20, CommissionCost: 10, SlippageCost: 10, SwapCost: 10,
	}
	pb2 := s.oppToProto(context.Background(), carry)
	if math.Abs(pb2.GapUsdPerUnit-0.0001) > eps {
		t.Errorf("carry gap_usd_per_unit want 0.0001, got %v", pb2.GapUsdPerUnit)
	}
	if math.Abs(pb2.NetUsdPerLotDay-3.0) > eps {
		t.Errorf("carry net_usd_per_lot_day want 3.0, got %v", pb2.NetUsdPerLotDay)
	}
	if pb2.NetUsdPerLot != 0 {
		t.Errorf("carry net_usd_per_lot want 0, got %v", pb2.NetUsdPerLot)
	}
	if math.Abs(pb2.CostUsdPerLot-0.5) > eps {
		t.Errorf("carry cost_usd_per_lot want 0.5, got %v", pb2.CostUsdPerLot)
	}

	// CS 缺失 → per-lot 全 0，gap 仍填。
	noCS := &Service{oppContractSize: func(_, _ string) (float64, bool) { return 0, false }}
	pb3 := noCS.oppToProto(context.Background(), cross)
	if math.Abs(pb3.GapUsdPerUnit-3.0) > eps {
		t.Errorf("no-CS gap_usd_per_unit want 3.0, got %v", pb3.GapUsdPerUnit)
	}
	if pb3.NetUsdPerLot != 0 || pb3.NetUsdPerLotDay != 0 || pb3.CostUsdPerLot != 0 {
		t.Errorf("no-CS per-lot want all 0, got net=%v day=%v cost=%v", pb3.NetUsdPerLot, pb3.NetUsdPerLotDay, pb3.CostUsdPerLot)
	}

	// 未接线（nil）→ 同 CS 缺失语义。
	bare := &Service{}
	pb4 := bare.oppToProto(context.Background(), cross)
	if pb4.NetUsdPerLot != 0 || pb4.CostUsdPerLot != 0 {
		t.Errorf("unwired per-lot want 0, got net=%v cost=%v", pb4.NetUsdPerLot, pb4.CostUsdPerLot)
	}
}

// --- H4.2 QtyBelowMinStep 单测 ---

// TestQtyBelowMinStep_MT5BelowStep MT5 qty 低于 volume step → true（静默跳过）。
func TestQtyBelowMinStep_MT5BelowStep(t *testing.T) {
	s := &Service{
		legVolumeStep: func(_, _ string) (float64, bool) { return 0.01, true },
	}
	if !s.QtyBelowMinStep("529:mt5", "AUDJPYm", 0.005) {
		t.Error("qty 0.005 < step 0.01 should be below min step")
	}
}

// TestQtyBelowMinStep_MT5AtOrAboveStep MT5 qty >= volume step → false（不跳过）。
func TestQtyBelowMinStep_MT5AtOrAboveStep(t *testing.T) {
	s := &Service{
		legVolumeStep: func(_, _ string) (float64, bool) { return 0.01, true },
	}
	if s.QtyBelowMinStep("529:mt5", "AUDJPYm", 0.01) {
		t.Error("qty 0.01 == step 0.01 should NOT be below min step")
	}
	if s.QtyBelowMinStep("529:mt5", "AUDJPYm", 1.0) {
		t.Error("qty 1.0 > step 0.01 should NOT be below min step")
	}
}

// TestQtyBelowMinStep_MT5UnknownStep MT5 未接线 volume step → 默认 0.01。
func TestQtyBelowMinStep_MT5UnknownStep(t *testing.T) {
	s := &Service{} // legVolumeStep=nil
	if !s.QtyBelowMinStep("529:mt5", "AUDJPYm", 0.005) {
		t.Error("qty 0.005 < default step 0.01 should be below min step (unknown → default)")
	}
	if s.QtyBelowMinStep("529:mt5", "AUDJPYm", 0.02) {
		t.Error("qty 0.02 > default step 0.01 should NOT be below min step (unknown → default)")
	}
}

// TestQtyBelowMinStep_CEXNotApplicable CEX 腿无 volume step 概念 → false（不跳过）。
func TestQtyBelowMinStep_CEXNotApplicable(t *testing.T) {
	s := &Service{
		legVolumeStep: func(_, _ string) (float64, bool) { return 0.01, true },
	}
	if s.QtyBelowMinStep("binance:spot", "BTCUSDT", 0.0001) {
		t.Error("CEX leg should NOT be subject to MT volume step check")
	}
}

// TestQtyBelowMinStep_ZeroOrNegative qty <= 0 → true（无效量跳过）。
func TestQtyBelowMinStep_ZeroOrNegative(t *testing.T) {
	s := &Service{}
	if !s.QtyBelowMinStep("529:mt5", "AUDJPYm", 0) {
		t.Error("qty=0 should be below min step (invalid)")
	}
	if !s.QtyBelowMinStep("529:mt5", "AUDJPYm", -1) {
		t.Error("qty<0 should be below min step (invalid)")
	}
}

// TestD331_OppToProtoHasOpenPosition T4：机会卡「已持仓」标识（D-331 S6）。
// ① 无 store（裸 Service）：HasOpenPosition=false 不 panic（fail-safe，同 fillVerifiedFields 姿态）；
// ② DSN：活跃仓（OPEN）命中=true；翻 CLOSED+缓存强制过期刷新后=false（TTL 刷新路径覆盖）。
func TestD331_OppToProtoHasOpenPosition(t *testing.T) {
	opp := &pgstore.Opportunity{
		ID: "XASSET_CARRY:XAUUSD:277842155:mt5:binance:perp", Type: "XASSET_CARRY",
		Canonical: "XAUUSD", VenueBuy: "277842155:mt5", VenueSell: "binance:perp", NetBps: 8.1,
	}
	// ① 无 store
	bare := &Service{}
	if pb := bare.oppToProto(context.Background(), opp); pb.HasOpenPosition {
		t.Error("无 store：HasOpenPosition 应 false（fail-safe）")
	}
	// ② DSN：OPEN 命中 → CLOSED 过期刷新后不命中
	st := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status) VALUES ($1, 'XASSET_CARRY', 'OPEN')`, opp.ID); err != nil {
		t.Fatalf("seed open position: %v", err)
	}
	s := &Service{store: st}
	if pb := s.oppToProto(ctx, opp); !pb.HasOpenPosition {
		t.Error("OPEN 仓命中：HasOpenPosition 应 true")
	}
	if _, err := st.Pool().Exec(ctx,
		`UPDATE positions SET status='CLOSED' WHERE opportunity_id=$1`, opp.ID); err != nil {
		t.Fatalf("close position: %v", err)
	}
	s.activeOppMu.Lock()
	s.activeOppAt = time.Time{} // 强制 TTL 过期（15s 窗口内测试不等钟）
	s.activeOppMu.Unlock()
	if pb := s.oppToProto(ctx, opp); pb.HasOpenPosition {
		t.Error("CLOSED 后（缓存过期刷新）：HasOpenPosition 应 false")
	}
}
