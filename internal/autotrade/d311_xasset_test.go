package autotrade

// D-311 S8 锚④⑥⑧⑨⑩：XASSET_CARRY 退出链（recalcXAssetNetCarry 双向泛化 +
// 去抖/minHold/缺数据 fail-closed）+ currentBasisBps 跨 canonical +
// env/liveGate CEX 合并视图 + handleOpportunity 前置拦截。

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
	"arb/internal/exec"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
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
	e := &Engine{store: s, cfg: Config{SwapFactMaxAge: 2 * time.Hour}}
	got, ok := e.recalcXAssetNetCarry(ctx, pos)
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
	e := &Engine{store: s, cfg: Config{SwapFactMaxAge: 2 * time.Hour}}
	got, ok := e.recalcXAssetNetCarry(ctx, pos)
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
		cfg: Config{
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
		cfg: Config{
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
		cfg:           Config{SwapFactMaxAge: 2 * time.Hour, FundingExitPct: 0, PerpFundExitDebounce: 1},
	}
	if _, ok := e.recalcXAssetNetCarry(ctx, pos); ok {
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
	e := &Engine{store: s, fundingBreach: map[int64]int{}, cfg: Config{SwapFactMaxAge: 2 * time.Hour, PerpFundExitDebounce: 1}}
	if _, ok := e.recalcXAssetNetCarry(ctx, pos); ok {
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

// TestD311_VenueIsRealGate_CEXAndMT S8锚⑨：ListVenueIsReal 合并视图——
// CEX venue（exchange_configs.simulated）开始可判；MT（broker_accounts）行为回归不变。
func TestD311_VenueIsRealGate_CEXAndMT(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	// MT demo 账户 + CEX：gate=实盘（simulated=f）、okx=模拟（simulated=t）。
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, platform, host, port, user_id, password, server, status, is_real)
		 VALUES ('vD', 1, 'h', 443, 1, 'p', 's', 'connected', false)`); err != nil {
		t.Fatalf("seed broker: %v", err)
	}
	for _, row := range []struct {
		venue     string
		simulated bool
	}{
		{"gate", false}, // → is_real=true
		{"okx", true},   // → is_real=false
	} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret, simulated)
			 VALUES ($1, 'perp', 'k', 's', $2)`, row.venue, row.simulated); err != nil {
			t.Fatalf("seed exchange %s: %v", row.venue, err)
		}
	}
	// env=demo：demo 腿（vD:mt5 + okx:perp）过；实盘腿 gate:perp 拒。
	g := &Gate{store: s, cfg: Config{Environment: "demo"}}
	if v := g.environmentIsRealCheck(ctx, TradeIntent{VenueBuy: "vD:mt5", VenueSell: "okx:perp"}); !v.Pass {
		t.Errorf("demo venues env=demo: want pass, got %s", v.Reason)
	}
	if v := g.environmentIsRealCheck(ctx, TradeIntent{VenueBuy: "vD:mt5", VenueSell: "gate:perp"}); v.Pass {
		t.Error("real CEX venue env=demo: want reject, got pass")
	}
	// env=live：gate:perp（真实盘）过。
	gLive := &Gate{store: s, cfg: Config{Environment: "live"}}
	if v := gLive.environmentIsRealCheck(ctx, TradeIntent{VenueBuy: "gate:perp", VenueSell: "gate:perp"}); !v.Pass {
		t.Errorf("real CEX venue env=live: want pass, got %s", v.Reason)
	}
	// liveGate（ARB_LIVE_TRADE=false）：gate:perp 实盘拦；okx:perp 模拟放行；vD:mt5 demo 放行（MT 回归）。
	gl := &Gate{store: s, cfg: Config{}}
	if v := gl.liveGateCheck(ctx, TradeIntent{VenueBuy: "gate:perp", VenueSell: "gate:perp"}); v.Pass {
		t.Error("liveGate real CEX: want reject, got pass")
	}
	if v := gl.liveGateCheck(ctx, TradeIntent{VenueBuy: "okx:perp", VenueSell: "vD:mt5"}); !v.Pass {
		t.Errorf("liveGate demo venues: want pass, got %s", v.Reason)
	}
	// 未知 venue fail-closed（MT 回归语义不变）。
	if v := gl.liveGateCheck(ctx, TradeIntent{VenueBuy: "ghost:mt5", VenueSell: "vD:mt5"}); v.Pass {
		t.Error("unknown venue: want reject (fail-closed), got pass")
	}
}

// TestD311_HandleOpportunityXAssetPreIntercept S8锚⑩：XASSET_CARRY 前置拦截——
// 无白名单命中 → 不产 intent 不落 REJECTED（修 462 条/时刷屏）；
// 命中 → 过拦截进入下游（csMiss 计数证明越过了拦截点）。
func TestD311_HandleOpportunityXAssetPreIntercept(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	// D-328 S3 涟漪适配：case③ 白名单命中后须过 regime 闸才到 sizing——种卖腿（gate:perp×BTCUSDT）
	// 健康正样本（requiredSign=+1 全程 ≥floor）。
	for i := 0; i < 13; i++ {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
			 VALUES ('funding', 'gate', 'BTCUSDT', $1, 'pct_annualized', $2, 'd311test')`,
			5.0+float64(i), time.Now().Add(-time.Duration(13-i)*5*time.Minute)); err != nil {
			t.Fatalf("seed funding fact %d: %v", i, err)
		}
	}
	opp := pgstore.Opportunity{
		ID: "XASSET_CARRY:WTIUSD:3001157:mt5:gate:perp", Type: "XASSET_CARRY",
		Canonical: "WTIUSD", CanonicalSell: d311strptr("BTCUSDT"),
		VenueBuy: "3001157:mt5", VenueSell: "gate:perp", NetBps: 50,
	}
	countLogs := func() int {
		var n int
		if err := s.Pool().QueryRow(ctx,
			`SELECT count(*) FROM autotrade_logs WHERE opportunity_id = $1`, opp.ID).Scan(&n); err != nil {
			t.Fatalf("count logs: %v", err)
		}
		return n
	}
	mkEngine := func(allow []exec.XAssetComboAllowSpec) *Engine {
		// dashboard.New 注入空 QuoteBus（裸 &Service{} 的 nil qbus 会在 USDPerLotForLeg panic）。
		svc := dashboard.New(nil, bus.New(), nil, nil, nil, 0, 0, 0, 0, 0, 0, nil, nil, nil, nil, nil, nil, 0, nil, nil, 0, 0, nil)
		return &Engine{
			store: s, service: svc,
			csMissCounts: map[string]int{}, csMissSent: map[string]time.Time{},
			cfg: Config{
				MinNetBps:         0,
				AllowVenues:       map[string]bool{"3001157:mt5": true, "gate:perp": true},
				XAssetAllowCombos: allow,
				// D-328 S3 涟漪适配：regime 闸在白名单之后，不配置=fail-closed 会拦住 case③；
				// 配置 + 下方种健康样本保持本锚原语义（白名单拦截点验证）。
				CarryRegime: exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12},
			},
		}
	}
	// ① 空表（零信任全拒）→ 不产 intent、不落 REJECTED
	e := mkEngine(nil)
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if n := countLogs(); n != 0 {
		t.Fatalf("空白名单应静默跳过, autotrade_logs 落了 %d 行", n)
	}
	// ② 非空表不命中 → 同静默跳过
	e = mkEngine([]exec.XAssetComboAllowSpec{
		{CanonicalBuy: "WTIUSD", CanonicalSell: "ETHUSDT", VenueBuy: "3001157:mt5", VenueSell: "gate:perp"},
	})
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if n := countLogs(); n != 0 {
		t.Fatalf("白名单不命中应静默跳过, autotrade_logs 落了 %d 行", n)
	}
	// ③ 命中 → 过拦截（走到 computeBuyQty → USDPerLotForLeg 不可用 → recordCSMiss）。
	// csMissCounts 递增=拦截已放行的直接证据；仍不落 REJECTED（CS-miss 是聚合告警非拒绝）。
	// D-423 涟漪适配：白名单命中后须过 venue 三元组入口闸才到 sizing——种本组合
	// （pair=BTCUSDT-WTI,cex=gate:perp,mt=3001157:mt5）顺价>noise 行。
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_xasset_monitor (pair, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct,
			tracking_error_bps, equiv_annual_pct, noise_bps, cex_venue, mt_venue)
		VALUES ('BTCUSDT-WTI', 4355, 4356, 4354, 4355, 12, 5, 3, 0.001, 21.9, 1.0, 24.9, 9,
			'gate:perp', '3001157:mt5')`); err != nil {
		t.Fatalf("seed monitor: %v", err)
	}
	e = mkEngine([]exec.XAssetComboAllowSpec{
		{CanonicalBuy: "WTIUSD", CanonicalSell: "BTCUSDT", VenueBuy: "3001157:mt5", VenueSell: "gate:perp", MaxLongQty: 1, MaxShortQty: 1000, MaxGrossUSD: 50000},
	})
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if e.csMissCounts["3001157:mt5|WTIUSD"] != 1 {
		t.Errorf("白名单命中应过拦截进入下游（csMiss=1）, got %v", e.csMissCounts)
	}
	if n := countLogs(); n != 0 {
		t.Fatalf("CS-miss 路径不应落 REJECTED, autotrade_logs 落了 %d 行", n)
	}
}
