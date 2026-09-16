package exec

// D-311 S8 锚⑤：XAssetComboAllow parse/match/空表全拒/名义超限拒 + canonical_sell
// 从 opportunities 行解析（pipeline 门禁层，需真库 seed opp 行）。

import (
	"context"
	"errors"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// d311ExecStore 真库桩（checkXAssetComboAllow 需 GetOpportunity 读 canonical_sell）。
func d311ExecStore(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_EXEC_TEST_PG_DSN")
	ctx := context.Background()
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// TestD311_ParseXAssetComboAllow 锚⑤a：7 段 record 解析——分号多 record；
// 段数错/数值非法行丢弃；空串 → nil（调用方据此全拒，零信任）。
func TestD311_ParseXAssetComboAllow(t *testing.T) {
	got := ParseXAssetComboAllow("")
	if got != nil {
		t.Fatalf("empty raw: want nil, got %+v", got)
	}
	raw := "WTIUSD|BTCUSDT|3001157:mt5|gate:perp|0.5|600|50000" +
		";XAUTUSDT|PAXGUSDT|binance:perp|binance:perp|1|2|100000"
	specs := ParseXAssetComboAllow(raw)
	if len(specs) != 2 {
		t.Fatalf("want 2 specs, got %d: %+v", len(specs), specs)
	}
	s0 := specs[0]
	if s0.CanonicalBuy != "WTIUSD" || s0.CanonicalSell != "BTCUSDT" ||
		s0.VenueBuy != "3001157:mt5" || s0.VenueSell != "gate:perp" ||
		s0.MaxLongQty != 0.5 || s0.MaxShortQty != 600 || s0.MaxGrossUSD != 50000 {
		t.Errorf("spec[0] parse wrong: %+v", s0)
	}
	// 非法行全部丢弃 → nil（不是部分保留——单 record 场景）
	bad := ParseXAssetComboAllow("A|B|v1:mt5|v2:perp|bad|2|3;X|Y|v1:mt5|v2:perp")
	if bad != nil {
		t.Fatalf("invalid records: want nil, got %+v", bad)
	}
	// 部分非法：好行保留坏行丢弃
	mixed := ParseXAssetComboAllow("WTIUSD|BTCUSDT|3001157:mt5|gate:perp|0.5|600|50000;bad|record")
	if len(mixed) != 1 {
		t.Fatalf("mixed: want 1 spec, got %d", len(mixed))
	}
}

// TestD311_MatchXAssetComboAllow 锚⑤b：精确匹配——四键全同才命中；
// canonicalSell/venue 任一不同 → nil。
func TestD311_MatchXAssetComboAllow(t *testing.T) {
	allow := []XAssetComboAllowSpec{
		{CanonicalBuy: "WTIUSD", CanonicalSell: "BTCUSDT", VenueBuy: "3001157:mt5", VenueSell: "gate:perp", MaxLongQty: 1, MaxShortQty: 1000, MaxGrossUSD: 50000},
	}
	if spec := MatchXAssetComboAllow(allow, "WTIUSD", "BTCUSDT", "3001157:mt5", "gate:perp"); spec == nil {
		t.Fatal("exact match: want hit, got nil")
	}
	for _, tc := range []struct{ cb, cs, vb, vs string }{
		{"WTIUSD", "WTIUSD", "3001157:mt5", "gate:perp"}, // canonicalSell 不同
		{"BTCUSDT", "BTCUSDT", "3001157:mt5", "gate:perp"},
		{"WTIUSD", "BTCUSDT", "999:mt5", "gate:perp"}, // venueBuy 不同
		{"WTIUSD", "BTCUSDT", "3001157:mt5", "okx:perp"},
	} {
		if spec := MatchXAssetComboAllow(allow, tc.cb, tc.cs, tc.vb, tc.vs); spec != nil {
			t.Errorf("non-match %s/%s %s×%s: want nil, got %+v", tc.cb, tc.cs, tc.vb, tc.vs, spec)
		}
	}
	// 空表全拒
	if spec := MatchXAssetComboAllow(nil, "WTIUSD", "BTCUSDT", "3001157:mt5", "gate:perp"); spec != nil {
		t.Error("empty allowlist: want nil (全拒), got spec")
	}
}

// TestD311_CheckXAssetComboAllow 锚⑤c：pipeline 门禁——canonical_sell 从 opp 行读；
// 空表全拒/qty 超限/gross 超限/CS 缺失 fail-closed/通过；canonical_sell NULL 回退。
func TestD311_CheckXAssetComboAllow(t *testing.T) {
	s := d311ExecStore(t)
	ctx := context.Background()
	oppID := "XASSET_CARRY:WTIUSD:3001157:mt5:gate:perp"
	cs := "BTCUSDT"
	if _, err := s.Pool().Exec(ctx, `TRUNCATE opportunities CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "WTIUSD", CanonicalSell: &cs,
		VenueBuy: "3001157:mt5", VenueSell: "gate:perp",
		QuoteTime: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed opportunity: %v", err)
	}
	mkAllow := func() []XAssetComboAllowSpec {
		return []XAssetComboAllowSpec{
			{CanonicalBuy: "WTIUSD", CanonicalSell: "BTCUSDT", VenueBuy: "3001157:mt5", VenueSell: "gate:perp",
				MaxLongQty: 1, MaxShortQty: 1000, MaxGrossUSD: 50000},
		}
	}
	legs := []LegSpec{
		{VenueKey: "3001157:mt5", VenueSymbol: "WTIUSD", Side: "BUY", Qty: 0.5, PriceExpected: 40},
		{VenueKey: "gate:perp", VenueSymbol: "BTC_USDT", Side: "SELL", Qty: 500, PriceExpected: 100000},
	}
	// buy 名义 = 0.5×100×40 = $2000；sell = 500×0.01×100000 = $500000 > $50000 → 超限。
	// 合规名义：sell qty=4 → 4×0.01×100000=$4000，gross=$6000 < $50000。
	legsOK := []LegSpec{
		{VenueKey: "3001157:mt5", VenueSymbol: "WTIUSD", Side: "BUY", Qty: 0.5, PriceExpected: 40},
		{VenueKey: "gate:perp", VenueSymbol: "BTC_USDT", Side: "SELL", Qty: 4, PriceExpected: 100000},
	}
	resolvers := func() *Pipeline {
		return &Pipeline{
			store:            s,
			xassetComboAllow: mkAllow(), // 每 case 独立 slice（case ④ 改 spec 不泄漏）
			csResolver:       &fakeCSResolver{sizes: map[string]float64{"3001157:mt5:WTIUSD": 100, "gate:perp:BTC_USDT": 0.01}},
			fxResolver:       fakeFxResolver{rates: map[string]float64{"USD": 1}, ok: true},
		}
	}
	// ① 空表全拒（零信任）
	p := &Pipeline{store: s, xassetComboAllow: nil}
	if err := p.checkXAssetComboAllow(ctx, oppID, legsOK); !errors.Is(err, ErrXAssetComboNotAllowed) {
		t.Errorf("empty allowlist: want ErrXAssetComboNotAllowed, got %v", err)
	}
	// ② 组合不命中（canonical_sell 不匹配）
	p = resolvers()
	p.xassetComboAllow = []XAssetComboAllowSpec{{CanonicalBuy: "WTIUSD", CanonicalSell: "ETHUSDT", VenueBuy: "3001157:mt5", VenueSell: "gate:perp"}}
	if err := p.checkXAssetComboAllow(ctx, oppID, legsOK); !errors.Is(err, ErrXAssetComboNotAllowed) {
		t.Errorf("mismatch canonical_sell: want ErrXAssetComboNotAllowed, got %v", err)
	}
	// ③ 名义超限（gross $500000 > $50000）
	p = resolvers()
	if err := p.checkXAssetComboAllow(ctx, oppID, legs); !errors.Is(err, ErrGrossExposureExceeds) {
		t.Errorf("gross exceed: want ErrGrossExposureExceeds, got %v", err)
	}
	// ④ qty 上限（long 0.5 > MaxLongQty 0.4）
	p = resolvers()
	p.xassetComboAllow[0].MaxLongQty = 0.4
	if err := p.checkXAssetComboAllow(ctx, oppID, legsOK); !errors.Is(err, ErrQtyExceedsLimit) {
		t.Errorf("long qty exceed: want ErrQtyExceedsLimit, got %v", err)
	}
	// ⑤ CS 缺失 fail-closed
	p = resolvers()
	p.csResolver = &fakeCSResolver{sizes: map[string]float64{}}
	if err := p.checkXAssetComboAllow(ctx, oppID, legsOK); !errors.Is(err, ErrComboNotAllowed) {
		t.Errorf("CS missing: want ErrComboNotAllowed, got %v", err)
	}
	// ⑥ 通过
	p = resolvers()
	if err := p.checkXAssetComboAllow(ctx, oppID, legsOK); err != nil {
		t.Errorf("within limits: want nil, got %v", err)
	}
	// ⑦ canonical_sell NULL → 回退 canonicalBuy（兼容非 XASSET 行）
	sameCanonID := "XASSET_CARRY:BTCUSDT:binance:perp:gate:perp"
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: sameCanonID, Type: "XASSET_CARRY", Canonical: "BTCUSDT", CanonicalSell: nil,
		VenueBuy: "binance:perp", VenueSell: "gate:perp",
		QuoteTime: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed same-canon opportunity: %v", err)
	}
	p = &Pipeline{
		store: s,
		xassetComboAllow: []XAssetComboAllowSpec{
			{CanonicalBuy: "BTCUSDT", CanonicalSell: "BTCUSDT", VenueBuy: "binance:perp", VenueSell: "gate:perp"},
		},
	}
	if err := p.checkXAssetComboAllow(ctx, sameCanonID, []LegSpec{
		{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "BUY", Qty: 0.1, PriceExpected: 100000},
		{VenueKey: "gate:perp", VenueSymbol: "BTC_USDT", Side: "SELL", Qty: 10, PriceExpected: 100000},
	}); err != nil {
		t.Errorf("canonical_sell NULL fallback: want nil, got %v", err)
	}
	// ⑧ venue 序向交换（D-311 返修锚）：ID 内 venue 对经 genID 字典序排序——
	// "3001157:mt5" < "gate:perp" 故 ID 序向与真实买卖方向相反。venue 须以行
	// VenueBuy/VenueSell 为准：行方向命中→放行；仅 ID 序向命中→拒（旧码会误放行反向组合）。
	swapOppID := "XASSET_CARRY:XPTUSD:3001157:mt5:gate:perp"
	csPaxg := "PAXGUSDT"
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: swapOppID, Type: "XASSET_CARRY", Canonical: "XPTUSD", CanonicalSell: &csPaxg,
		VenueBuy: "gate:perp", VenueSell: "3001157:mt5",
		QuoteTime: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed swapped-venue opportunity: %v", err)
	}
	swapLegs := []LegSpec{
		{VenueKey: "gate:perp", VenueSymbol: "XPT_USDT", Side: "BUY", Qty: 1, PriceExpected: 1000},
		{VenueKey: "3001157:mt5", VenueSymbol: "PAXGUSD", Side: "SELL", Qty: 1, PriceExpected: 2500},
	}
	p = &Pipeline{store: s, xassetComboAllow: []XAssetComboAllowSpec{
		{CanonicalBuy: "XPTUSD", CanonicalSell: "PAXGUSDT", VenueBuy: "gate:perp", VenueSell: "3001157:mt5"},
	}}
	if err := p.checkXAssetComboAllow(ctx, swapOppID, swapLegs); err != nil {
		t.Errorf("row-direction match: want nil, got %v", err)
	}
	p = &Pipeline{store: s, xassetComboAllow: []XAssetComboAllowSpec{
		{CanonicalBuy: "XPTUSD", CanonicalSell: "PAXGUSDT", VenueBuy: "3001157:mt5", VenueSell: "gate:perp"},
	}}
	if err := p.checkXAssetComboAllow(ctx, swapOppID, swapLegs); !errors.Is(err, ErrXAssetComboNotAllowed) {
		t.Errorf("id-sorted direction only: want ErrXAssetComboNotAllowed, got %v", err)
	}
	// ⑨ 行缺失 fail-closed（返修：无机会行不得靠排序 ID 猜方向）
	p = &Pipeline{store: s, xassetComboAllow: mkAllow()}
	if err := p.checkXAssetComboAllow(ctx, "XASSET_CARRY:ZZZ:gone:mt5:gone:perp", legsOK); !errors.Is(err, ErrXAssetComboNotAllowed) {
		t.Errorf("missing opp row: want ErrXAssetComboNotAllowed, got %v", err)
	}
}
