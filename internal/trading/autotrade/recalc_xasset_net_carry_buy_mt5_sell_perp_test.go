package autotrade

// 锚④⑥⑧⑨⑩：XASSET_CARRY 退出链（recalcXAssetNetCarry 双向泛化 +
// 去抖/minHold/缺数据 fail-closed）+ currentBasisBps 跨 canonical +
// env/liveGate CEX 合并视图 + handleOpportunity 前置拦截。

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// d311SeedXAssetPosition 建 XASSET_CARRY 持仓+双腿。
// buyLeg=(buyVenue,buySym) SELL 侧同理；oppID 组合键须与 canonicalBuy 一致。
func d311SeedXAssetPosition(t *testing.T, s *pgstore.Store, oppID, buyVenue, buySym, sellVenue, sellSym string) *pgstore.Position {
	t.Helper()
	ctx := context.Background()
	pos := &pgstore.Position{OpportunityID: oppID, Type: "XASSET_CARRY", Status: "OPEN"}
	id, err := s.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("create xasset position: %v", err)
	}
	pos.ID = id
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 1, Phase: "OPEN", Venue: buyVenue, Symbol: buySym,
		Side: "BUY", Qty: 1.0, Status: "FILLED",
	}); err != nil {
		t.Fatalf("create buy leg: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 2, Phase: "OPEN", Venue: sellVenue, Symbol: sellSym,
		Side: "SELL", Qty: 1.0, Status: "FILLED",
	}); err != nil {
		t.Fatalf("create sell leg: %v", err)
	}
	return pos
}

func d311Truncate(t *testing.T, s *pgstore.Store) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(),
		`TRUNCATE facts, positions, position_legs, opportunities, broker_symbol_map, broker_accounts, exchange_configs, autotrade_logs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func d311strptr(s string) *string { return &s }

// TestD311_RecalcXAssetNetCarry_BuyMT5SellPerp S8锚⑧a：XPTUSD 形态——
// 买 MT5（swap spec 2.0 bps/日 → +7.3%/yr）+ 卖 OKX perp（funding −10%/yr → 空头收 −10）。
// netCarry = 7.3 − 10 = −2.7（持有净收入口径：SELL 腿 +rate）。
func TestD311_RecalcXAssetNetCarry_BuyMT5SellPerp(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	oppID := "XASSET_CARRY:XPTUSD:3001157:mt5:okx:perp"
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "XPTUSD", CanonicalSell: d311strptr("XPTUSDT"),
		VenueBuy: "3001157:mt5", VenueSell: "okx:perp", ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed opp: %v", err)
	}
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "3001157", Symbol: "XPTUSD", Value: 2.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindFunding, Venue: "okx", Symbol: "XPTUSDT", Value: -10.0, Unit: fact.UnitPctAnnualized, Ts: now, Src: "okx"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	pos := d311SeedXAssetPosition(t, s, oppID, "3001157:mt5", "XPTUSD", "okx:perp", "XPT-USDT-SWAP")
	e := &Engine{store: s, cfg: gate.Config{SwapFactMaxAge: 2 * time.Hour}}
	got, ok := e.recalcXAssetNetCarry(ctx, pos, 0, "exit")
	if !ok {
		t.Fatal("want ok=true, got false")
	}
	want := 2.0*3.65 + (-10.0) // 7.3 − 10 = −2.7
	if absFloat(got-want) > 1e-9 {
		t.Errorf("netCarry want %.4f, got %.4f", want, got)
	}
}

// TestD311_RecalcXAssetNetCarry_BuyPerpSellMT5 S8锚⑧b：反向形态——
// 买 binance perp（funding +5%/yr → 多头付 5）+ 卖 MT5（swap_short −1.0 bps/日 → −3.65%/yr）。
// netCarry = −5 − 3.65 = −8.65（BUY 腿 −rate）。
func TestD311_RecalcXAssetNetCarry_BuyPerpSellMT5(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	oppID := "XASSET_CARRY:BTCUSDT:binance:perp:3001157:mt5"
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "BTCUSDT", CanonicalSell: d311strptr("BTCUSD"),
		VenueBuy: "binance:perp", VenueSell: "3001157:mt5", ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed opp: %v", err)
	}
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindFunding, Venue: "binance", Symbol: "BTCUSDT", Value: 5.0, Unit: fact.UnitPctAnnualized, Ts: now, Src: "binance"},
		{Kind: fact.KindSwapShort, Venue: "3001157", Symbol: "BTCUSD", Value: -1.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	pos := d311SeedXAssetPosition(t, s, oppID, "binance:perp", "BTCUSDT", "3001157:mt5", "BTCUSD")
	e := &Engine{store: s, cfg: gate.Config{SwapFactMaxAge: 2 * time.Hour}}
	got, ok := e.recalcXAssetNetCarry(ctx, pos, 0, "exit")
	if !ok {
		t.Fatal("want ok=true, got false")
	}
	want := -5.0 + (-1.0 * 3.65) // −5 − 3.65 = −8.65
	if absFloat(got-want) > 1e-9 {
		t.Errorf("netCarry want %.4f, got %.4f", want, got)
	}
}

// d311XAssetExitFixture 退出链公共夹具：XPTUSD 形态持仓 + 负 netCarry facts
// （funding −10 + swap 0 → netCarry=−10 ≤ exitPct=0 → breach）。
func d311XAssetExitFixture(t *testing.T, s *pgstore.Store, fundingVal float64) *pgstore.Position {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	oppID := "XASSET_CARRY:XPTUSD:3001157:mt5:okx:perp"
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "XPTUSD", CanonicalSell: d311strptr("XPTUSDT"),
		VenueBuy: "3001157:mt5", VenueSell: "okx:perp", ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed opp: %v", err)
	}
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "3001157", Symbol: "XPTUSD", Value: 0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindFunding, Venue: "okx", Symbol: "XPTUSDT", Value: fundingVal, Unit: fact.UnitPctAnnualized, Ts: now, Src: "okx"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	return d311SeedXAssetPosition(t, s, oppID, "3001157:mt5", "XPTUSD", "okx:perp", "XPT-USDT-SWAP")
}

// TestD311_XAssetExit_FundingFlipDebounce S8锚④主锚：净收入转负 → 连续去抖 N 次后才平。
// minHold=0、debounce=2：第 1 次 breach 不平（计数 1），第 2 次平。
func TestD311_XAssetExit_FundingFlipDebounce(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	pos := d311XAssetExitFixture(t, s, -10.0) // netCarry = −10 → breach
	e := &Engine{
		store:         s,
		fundingBreach: map[int64]int{},
		cfg: gate.Config{
			SwapFactMaxAge:       2 * time.Hour,
			FundingExitPct:       0,
			PerpFundMinHold:      0,
			PerpFundExitDebounce: 2,
		},
	}
	ctx := context.Background()
	if close, reason := e.checkExitCondition(ctx, pos); close {
		t.Fatalf("首次 breach 不应平（去抖 1/2）, reason=%s", reason)
	}
	if close, reason := e.checkExitCondition(ctx, pos); !close {
		t.Fatalf("连续第 2 次 breach 应平, reason=%s", reason)
	}
}

// TestD311_XAssetExit_MinHoldNotMet S8锚④：持有未满 minHold → breach 不平且计数清零。
func TestD311_XAssetExit_MinHoldNotMet(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	pos := d311XAssetExitFixture(t, s, -10.0) // breach 但 opened_at=now < minHold
	e := &Engine{
		store:         s,
		fundingBreach: map[int64]int{pos.ID: 5}, // 预置计数——floor 未满应清零
		cfg: gate.Config{
			SwapFactMaxAge:       2 * time.Hour,
			FundingExitPct:       0,
			PerpFundMinHold:      8 * time.Hour,
			PerpFundExitDebounce: 1,
		},
	}
	if close, _ := e.checkExitCondition(context.Background(), pos); close {
		t.Fatal("minHold 未满不应平")
	}
	if e.fundingBreach[pos.ID] != 0 {
		t.Errorf("minHold 未满应清零计数, got %d", e.fundingBreach[pos.ID])
	}
}

// TestD311_XAssetExit_MissingFactNoDecision S8锚④：缺 funding fact → ok=false → 不平不计数。
func TestD311_XAssetExit_MissingFactNoDecision(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	oppID := "XASSET_CARRY:XPTUSD:3001157:mt5:okx:perp"
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "XPTUSD", CanonicalSell: d311strptr("XPTUSDT"),
		VenueBuy: "3001157:mt5", VenueSell: "okx:perp", ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed opp: %v", err)
	}
	// 只插 MT 腿 swap fact，不插 okx funding fact。
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "3001157", Symbol: "XPTUSD", Value: 2.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	pos := d311SeedXAssetPosition(t, s, oppID, "3001157:mt5", "XPTUSD", "okx:perp", "XPT-USDT-SWAP")
	e := &Engine{
		store:         s,
		fundingBreach: map[int64]int{pos.ID: 3},
		cfg:           gate.Config{SwapFactMaxAge: 2 * time.Hour, FundingExitPct: 0, PerpFundExitDebounce: 1},
	}
	if _, ok := e.recalcXAssetNetCarry(ctx, pos, 0, "exit"); ok {
		t.Fatal("缺 funding fact 应 ok=false")
	}
	if close, _ := e.checkExitCondition(ctx, pos); close {
		t.Fatal("缺 fact 不应平（fail-closed 不决策）")
	}
	if e.fundingBreach[pos.ID] != 3 {
		t.Errorf("缺 fact 计数应原样保留(3), got %d", e.fundingBreach[pos.ID])
	}
}

// TestD311_XAssetExit_MissingMapNoDecision S8锚④：canonical_sell 无 opp 行且
// broker_symbol_map 无映射 → ok=false（不猜 canonical）。
func TestD311_XAssetExit_MissingMapNoDecision(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "3001157", Symbol: "XPTUSD", Value: 2.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindFunding, Venue: "okx", Symbol: "XPTUSDT", Value: -10.0, Unit: fact.UnitPctAnnualized, Ts: now, Src: "okx"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	// 不 seed opportunity 行、不 seed broker_symbol_map → 卖腿 canonical 不可解析。
	pos := d311SeedXAssetPosition(t, s,
		"XASSET_CARRY:XPTUSD:3001157:mt5:okx:perp",
		"3001157:mt5", "XPTUSD", "okx:perp", "XPT-USDT-SWAP")
	e := &Engine{store: s, fundingBreach: map[int64]int{}, cfg: gate.Config{SwapFactMaxAge: 2 * time.Hour, PerpFundExitDebounce: 1}}
	if _, ok := e.recalcXAssetNetCarry(ctx, pos, 0, "exit"); ok {
		t.Fatal("canonical_sell 不可解析应 ok=false")
	}
	if close, _ := e.checkExitCondition(ctx, pos); close {
		t.Fatal("map 缺失不应平（fail-closed）")
	}
}

// TestD311_XAssetSellCanonical_MapFallback 补充锚：opp 行缺失时经
// broker_symbol_map 兜底解析卖腿 canonical。
func TestD311_XAssetSellCanonical_MapFallback(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ('okx','mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING`); err != nil {
		t.Fatalf("seed broker: %v", err)
	}
	if err := s.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{
		Venue: "okx", BrokerSymbol: "XPT-USDT-SWAP", Canonical: "XPTUSDT", Source: "manual",
	}); err != nil {
		t.Fatalf("seed map: %v", err)
	}
	pos := d311SeedXAssetPosition(t, s,
		"XASSET_CARRY:XPTUSD:3001157:mt5:okx:perp",
		"3001157:mt5", "XPTUSD", "okx:perp", "XPT-USDT-SWAP")
	e := &Engine{store: s}
	legs, err := s.ListPositionLegs(ctx, pos.ID)
	if err != nil {
		t.Fatalf("list legs: %v", err)
	}
	if got := e.xassetSellCanonical(ctx, pos, legs); got != "XPTUSDT" {
		t.Errorf("map fallback: want XPTUSDT, got %q", got)
	}
}

// TestD311_CurrentBasisBps_CrossCanonical S8锚⑥：双腿各自 canonical 取报价——
// 买腿 XAUTUSDT@gate:perp、卖腿 PAXGUSD@3001157:mt5（canonical_sell 经 opp 行）。
// basis = (buy.Bid − sell.Ask)/ref×10000；同所跨 canonical 不拿买 canonical 错查。
func TestD311_CurrentBasisBps_CrossCanonical(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	oppID := "XASSET_CARRY:XAUTUSDT:gate:perp:3001157:mt5"
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "XAUTUSDT", CanonicalSell: d311strptr("PAXGUSD"),
		VenueBuy: "gate:perp", VenueSell: "3001157:mt5", ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed opp: %v", err)
	}
	pos := d311SeedXAssetPosition(t, s, oppID, "gate:perp", "XAUTUSDT", "3001157:mt5", "PAXGUSD")
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "XAUTUSDT", Venue: "gate", VenueType: "perp", Bid: 4000, Ask: 4000.2, Time: now, VenueSymbol: "XAUTUSDT"})
	qb.Publish(bus.Quote{Symbol: "PAXGUSD", Venue: "3001157", VenueType: "mt5", Bid: 3999, Ask: 3999.2, Time: now, VenueSymbol: "PAXGUSD"})
	e := &Engine{store: s, quotes: qb}
	basis, ok := e.currentBasisBps(pos)
	if !ok {
		t.Fatal("want ok=true, got false")
	}
	// spread = buy.Bid 4000 − sell.Ask 3999.2 = 0.8；ref = buy.Ask 4000.2 → 0.8/4000.2×10000 ≈ 1.9999
	want := (4000 - 3999.2) / 4000.2 * 10000
	if absFloat(basis-want) > 0.01 {
		t.Errorf("basis want %.4f, got %.4f", want, basis)
	}
	// 反向锚：若实现错拿买 canonical 查卖腿（XAUTUSDT@3001157:mt5 无报价）→ ok=false。
	// 上面 ok=true + 值正确即证明双腿 canonical 分别生效。
}
