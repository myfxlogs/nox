package dashboard

// D-391 S3：回本列「本」口径重定义锚 A/B/C。
// 新口径：本=入场摩擦（点差付出+fee_usd+MT commission）；剩余=本−息差实收；÷当前日息率。
// 先红后绿：旧口径 net=floating+实收+commission（盯市噪声入本）下断言全部不成立。

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// d391Pos498Legs 498 型 fixture（生产实拍 9-22）：MT5 XAUUSDm buy 4356.863 +
// okx:perp XAU-USDT-SWAP sell 4361.9 → 成交价点差 −11.56bps（点差为赚→本项计 0）。
func d391Pos498Legs(fee *float64) []pgstore.PositionLeg {
	return []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "3001157:mt5", Symbol: "XAUUSDm", Side: "BUY", Status: "FILLED", Qty: 0.01, PriceActual: ptrFloat(4356.863), Ts: time.Now().AddDate(0, 0, -1)},
		{Phase: "OPEN", Venue: "okx:perp", Symbol: "XAU-USDT-SWAP", Side: "SELL", Status: "FILLED", Qty: 1000, PriceActual: ptrFloat(4361.9), FeeUSD: fee, Ts: time.Now().AddDate(0, 0, -1)},
	}
}

// 锚 A：498 型（点差为赚 spread<0、fee 有值、实收>0）→ 本=2.18（仅 fee），
// 剩余=2.18−1.10=1.08，breakEven=1.08÷1.10≈0.98 天。
// 旧口径：net=0+1.10+0=1.10≥0 → breakEven=0（红=值不同）。
func TestD391_BreakEven_SpreadEarned_FeeOnly(t *testing.T) {
	s := &Service{xassetCexFeeBps: 5.0}
	now := time.Now()
	pb := &v1.Position{
		Id: 498, OpportunityId: "XASSET_CARRY:XAUUSD:XAUTUSDT:okx:perp:3001157:mt5",
		Type: "XASSET_CARRY", Status: "OPEN",
		OpenedAt: timestamppb.New(now.AddDate(0, 0, -1)),
	}
	income := map[string]float64{"498\x00okx:perp": 1.10}
	enrichOne(context.Background(), s, pb, d391Pos498Legs(ptrFloat(2.18)), nil, now, nil, income, nil)
	if pb.BreakEvenDays == nil {
		t.Fatal("break_even_days should not be nil")
	}
	want := (2.18 - 1.10) / 1.10 // ≈0.982
	if math.Abs(*pb.BreakEvenDays-want) > 0.01 {
		t.Errorf("break_even_days = %.4f, want %.4f（本=2.18 费，点差赚到计 0）", *pb.BreakEvenDays, want)
	}
}

// 锚 C：实收 ≥ 本 → 已回本（remaining≤0 → breakEven=0）。
func TestD391_BreakEven_AlreadyCovered(t *testing.T) {
	s := &Service{xassetCexFeeBps: 5.0}
	now := time.Now()
	pb := &v1.Position{
		Id: 498, OpportunityId: "XASSET_CARRY:XAUUSD:XAUTUSDT:okx:perp:3001157:mt5",
		Type: "XASSET_CARRY", Status: "OPEN",
		OpenedAt: timestamppb.New(now.AddDate(0, 0, -1)),
	}
	income := map[string]float64{"498\x00okx:perp": 5.0} // 实收 5.0 > 本 2.18
	enrichOne(context.Background(), s, pb, d391Pos498Legs(ptrFloat(2.18)), nil, now, nil, income, nil)
	if pb.BreakEvenDays == nil {
		t.Fatal("break_even_days should not be nil")
	}
	if *pb.BreakEvenDays != 0 {
		t.Errorf("实收≥本 break_even_days want 0（已回本）, got %v", *pb.BreakEvenDays)
	}
}

// 锚 B：点差为付（spread>0）→ 本含点差成本。需真库（notional 走 CS/fx 查询，
// 查不到 → cs=1.0/fx=1.0 仍出名义）。
// buy@100.5 / sell@100 → spread=49.751bps；notional=1×100.5+1×100=200.5 →
// 点差本≈0.9975；实收 0.5 → remaining≈0.4975 → breakEven≈0.995。
// 旧口径：net=0.5≥0 → 0（红）。
func TestD391_BreakEven_SpreadPaid(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	s := &Service{store: st, xassetCexFeeBps: 5.0}
	now := time.Now()
	pb := &v1.Position{
		Id: 701, OpportunityId: "SWAPARB:XAUUSD:fakea:mt5:fakeb:mt5",
		Type: "SWAPARB", Status: "OPEN",
		OpenedAt: timestamppb.New(now.AddDate(0, 0, -1)),
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "fakea:mt5", Symbol: "XAUUSD", Side: "BUY", Status: "FILLED", Qty: 1, PriceActual: ptrFloat(100.5), Ts: now.AddDate(0, 0, -1)},
		{Phase: "OPEN", Venue: "fakeb:mt5", Symbol: "XAUUSD", Side: "SELL", Status: "FILLED", Qty: 1, PriceActual: ptrFloat(100.0), Ts: now.AddDate(0, 0, -1)},
	}
	snap := []*v1.BrokerPosition{
		{VenueKey: "fakea:mt5", Symbol: "XAUUSD", Swap: 0.25, PositionId: 701},
		{VenueKey: "fakeb:mt5", Symbol: "XAUUSD", Swap: 0.25, PositionId: 701},
	}
	enrichOne(context.Background(), s, pb, legs, snap, now, nil, nil, nil)
	if pb.BreakEvenDays == nil {
		t.Fatal("break_even_days should not be nil")
	}
	spreadBps := (100.5 - 100.0) / 100.5 * 10000
	spreadCost := spreadBps / 10000 * (100.5 + 100.0)
	want := (spreadCost - 0.5) / 0.5 // ≈0.995
	if math.Abs(*pb.BreakEvenDays-want) > 0.02 {
		t.Errorf("break_even_days = %.4f, want %.4f（本含点差成本 %.4f）", *pb.BreakEvenDays, want, spreadCost)
	}
}

// 锚 A 补：CEX 腿 fee_usd NULL → cfg.XAssetCexFeeBps 估算回退（诚实性，防本=0 假回本）。
// cex 腿 @100 qty1 → notional=100（cs 缺省 1.0），估费=100×5bps=0.05；
// 实收 0.025 → remaining 0.025 → breakEven=1.0。
func TestD391_BreakEven_FeeNullFallback(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	s := &Service{store: st, xassetCexFeeBps: 5.0}
	now := time.Now()
	pb := &v1.Position{
		Id: 702, OpportunityId: "XASSET_CARRY:XAUUSD:XAUTUSDT:okx:spot:fakeb:mt5",
		Type: "XASSET_CARRY", Status: "OPEN",
		OpenedAt: timestamppb.New(now.AddDate(0, 0, -1)),
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "fakeb:mt5", Symbol: "XAUUSD", Side: "BUY", Status: "FILLED", Qty: 1, PriceActual: ptrFloat(100.0), Ts: now.AddDate(0, 0, -1)},
		{Phase: "OPEN", Venue: "okx:spot", Symbol: "XAUT-USDT", Side: "SELL", Status: "FILLED", Qty: 1, PriceActual: ptrFloat(100.0), Ts: now.AddDate(0, 0, -1)}, // FeeUSD=nil → 估算
	}
	income := map[string]float64{"702\x00okx:spot": 0.025}
	enrichOne(context.Background(), s, pb, legs, nil, now, nil, income, nil)
	if pb.BreakEvenDays == nil {
		t.Fatal("break_even_days should not be nil（fee NULL 应走估算回退而非 0）")
	}
	want := (100.0*5.0/10000 - 0.025) / 0.025 // =1.0
	if math.Abs(*pb.BreakEvenDays-want) > 0.01 {
		t.Errorf("break_even_days = %.4f, want %.4f（fee NULL→估算 5bps）", *pb.BreakEvenDays, want)
	}
}

// === D-391 R1 返修锚：CEX 费双通道互斥（fee_usd > 快照 commission > 估算）===

// R1-A：CEX 腿 fee_usd=nil + 快照 bp.Commission=−2.0 → 本=2.0（快照真费），
// 估算不得叠加（估算路径若误触发本≈4.18 → 断言分差即红）。
func TestD391R1_SnapshotFeePreferred(t *testing.T) {
	s := &Service{xassetCexFeeBps: 5.0}
	now := time.Now()
	pb := &v1.Position{
		Id: 498, OpportunityId: "XASSET_CARRY:XAUUSD:XAUTUSDT:okx:perp:3001157:mt5",
		Type: "XASSET_CARRY", Status: "OPEN",
		OpenedAt: timestamppb.New(now.AddDate(0, 0, -1)),
	}
	snap := []*v1.BrokerPosition{
		{VenueKey: "okx:perp", Symbol: "XAU-USDT-SWAP", Commission: -2.0, PositionId: 498},
	}
	income := map[string]float64{"498\x00okx:perp": 1.10}
	enrichOne(context.Background(), s, pb, d391Pos498Legs(nil), snap, now, nil, income, nil)
	if pb.BreakEvenDays == nil {
		t.Fatal("break_even_days should not be nil")
	}
	want := (2.0 - 1.10) / 1.10 // ≈0.818；双计 ≈1.80、纯估算 ≈0.98 都会红
	if math.Abs(*pb.BreakEvenDays-want) > 0.01 {
		t.Errorf("break_even_days = %.4f, want %.4f（快照真费 −2.0 单笔计一次）", *pb.BreakEvenDays, want)
	}
}

// R1-B：fee_usd=2.18 + 快照 bp.Commission=−2.18 → 本=2.18 仅一次（fee_usd 优先）。
func TestD391R1_FeeUSDOverSnapshot(t *testing.T) {
	s := &Service{xassetCexFeeBps: 5.0}
	now := time.Now()
	pb := &v1.Position{
		Id: 498, OpportunityId: "XASSET_CARRY:XAUUSD:XAUTUSDT:okx:perp:3001157:mt5",
		Type: "XASSET_CARRY", Status: "OPEN",
		OpenedAt: timestamppb.New(now.AddDate(0, 0, -1)),
	}
	snap := []*v1.BrokerPosition{
		{VenueKey: "okx:perp", Symbol: "XAU-USDT-SWAP", Commission: -2.18, PositionId: 498},
	}
	income := map[string]float64{"498\x00okx:perp": 1.10}
	enrichOne(context.Background(), s, pb, d391Pos498Legs(ptrFloat(2.18)), snap, now, nil, income, nil)
	if pb.BreakEvenDays == nil {
		t.Fatal("break_even_days should not be nil")
	}
	want := (2.18 - 1.10) / 1.10 // ≈0.982；双计 ≈2.96 会红
	if math.Abs(*pb.BreakEvenDays-want) > 0.01 {
		t.Errorf("break_even_days = %.4f, want %.4f（fee_usd 优先不叠快照）", *pb.BreakEvenDays, want)
	}
}

// R1-C：CEX 腿 fee_usd=nil + 快照无该腿 bp → 估算回退照常（真库 notional）。
func TestD391R1_EstimateFallbackKeeps(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	s := &Service{store: st, xassetCexFeeBps: 5.0}
	now := time.Now()
	pb := &v1.Position{
		Id: 703, OpportunityId: "XASSET_CARRY:XAUUSD:XAUTUSDT:okx:spot:fakeb:mt5",
		Type: "XASSET_CARRY", Status: "OPEN",
		OpenedAt: timestamppb.New(now.AddDate(0, 0, -1)),
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "fakeb:mt5", Symbol: "XAUUSD", Side: "BUY", Status: "FILLED", Qty: 1, PriceActual: ptrFloat(100.0), Ts: now.AddDate(0, 0, -1)},
		{Phase: "OPEN", Venue: "okx:spot", Symbol: "XAUT-USDT", Side: "SELL", Status: "FILLED", Qty: 1, PriceActual: ptrFloat(100.0), Ts: now.AddDate(0, 0, -1)},
	}
	snap := []*v1.BrokerPosition{
		{VenueKey: "fakeb:mt5", Symbol: "XAUUSD", PositionId: 703}, // MT 腿有快照（swap=0）、CEX 腿无 → 回退估算
	}
	income := map[string]float64{"703\x00okx:spot": 0.025}
	enrichOne(context.Background(), s, pb, legs, snap, now, nil, income, nil)
	if pb.BreakEvenDays == nil {
		t.Fatal("break_even_days should not be nil（快照无覆盖腿应走估算）")
	}
	want := (100.0*5.0/10000 - 0.025) / 0.025 // =1.0
	if math.Abs(*pb.BreakEvenDays-want) > 0.01 {
		t.Errorf("break_even_days = %.4f, want %.4f（快照无覆盖 → 估算回退）", *pb.BreakEvenDays, want)
	}
}

// R1-D：MT 腿负 commission 计入本（回归保护）；正 commission（返佣）不计。
func TestD391R1_MTCommissionSign(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	s := &Service{store: st, xassetCexFeeBps: 5.0}
	now := time.Now()
	pb := &v1.Position{
		Id: 704, OpportunityId: "SWAPARB:XAUUSD:fakea:mt5:fakeb:mt5",
		Type: "SWAPARB", Status: "OPEN",
		OpenedAt: timestamppb.New(now.AddDate(0, 0, -1)),
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "fakea:mt5", Symbol: "XAUUSD", Side: "BUY", Status: "FILLED", Qty: 1, PriceActual: ptrFloat(100.0), Ts: now.AddDate(0, 0, -1)},
		{Phase: "OPEN", Venue: "fakeb:mt5", Symbol: "XAUUSD", Side: "SELL", Status: "FILLED", Qty: 1, PriceActual: ptrFloat(100.0), Ts: now.AddDate(0, 0, -1)},
	}
	snap := []*v1.BrokerPosition{
		{VenueKey: "fakea:mt5", Symbol: "XAUUSD", Swap: 0.05, Commission: -0.30, PositionId: 704}, // 负=付出 → 计入
		{VenueKey: "fakeb:mt5", Symbol: "XAUUSD", Swap: 0.05, Commission: 0.50, PositionId: 704},  // 正=返佣 → 不计
	}
	enrichOne(context.Background(), s, pb, legs, snap, now, nil, nil, nil)
	if pb.BreakEvenDays == nil {
		t.Fatal("break_even_days should not be nil")
	}
	// 本=0.30（返佣腿不抵）；实收=0.10 → remaining=0.20 → 0.20/0.10=2.0 天。
	// 若返佣误冲抵 → 本=−0.20 → 已回本 0 红；若负佣漏计 → 本=0 → 0 红。
	want := 2.0
	if math.Abs(*pb.BreakEvenDays-want) > 0.01 {
		t.Errorf("break_even_days = %v, want %v（负佣计入/返佣不计）", *pb.BreakEvenDays, want)
	}
}
