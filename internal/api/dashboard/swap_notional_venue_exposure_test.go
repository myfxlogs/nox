package dashboard

// swap_notional_venue_exposure_test.go — D-132 T1 venue 级敞口门单测（四用例）。
// 设计 SSOT：docs/SWAPARB扩产方案-D-132-施工提示词.md §3 T1。
//
// 用例：
// ① 3 仓活 + 新仓超 cap → skip（纯函数 swapArbVenueExposureCheck）
// ② cap 内 → ok（纯函数）
// ③ venue 离线 → fail-closed（Service.SwapArbVenueExposureOK + acctMon）
// ④ cap=0 → 不启用（纯函数，等价 bareCap≤0 先例语义）

import (
	"context"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/ledger/reconcile"
	"arb/internal/store/pgstore"
)

// mockUSDPerLotForLeg 构造 usdPerLotOf 回调：固定 canonical+venue → usdPerLot（ok=true）。
func mockUSDPerLotForLeg(perLot float64) func(canonical, venue string) (float64, bool) {
	return func(_, _ string) (float64, bool) { return perLot, true }
}

// threeSwapArbLegs 构造 3 个 SWAPARB 活仓的 OPEN 腿（每仓 long+short 两腿）。
// 每腿 qty=0.01，venueLong="529:mt5"，venueShort="277:mt5"。
func threeSwapArbLegs() []pgstore.PositionLeg {
	var legs []pgstore.PositionLeg
	for i := int64(1); i <= 3; i++ {
		legs = append(legs,
			pgstore.PositionLeg{PositionID: i, Phase: "OPEN", Venue: "529:mt5", Qty: 0.01},
			pgstore.PositionLeg{PositionID: i, Phase: "OPEN", Venue: "277:mt5", Qty: 0.01},
		)
	}
	return legs
}

// canonicalByPosStub 构造 canonicalOf 回调：posID → "AUDJPY"（全命中）。
func canonicalByPosStub(_ int64) string { return "AUDJPY" }

// TestSwapArbVenueExposureCheck_OverCapSkip ① 3 仓活 + 新仓超 cap → false（skip）。
// 3 仓 × 0.01 手 × $76000/手 = $2280 现有裸露（venueShort 侧）；equity $1500 × cap 2.0 = $3000 限额。
// D-174 leverage=1.0：保证金=名义/1=名义。新仓 $760 → $2280 + $760 = $3040 > $3000 → skip。
func TestSwapArbVenueExposureCheck_OverCapSkip(t *testing.T) {
	got := swapArbVenueExposureCheck(
		threeSwapArbLegs(),
		canonicalByPosStub,
		mockUSDPerLotForLeg(76000),
		"529:mt5", "277:mt5",
		760,  // newNotional
		1500, // eqLong
		1500, // eqShort
		2.0,  // cap
		1.0,  // longLev（D-174：1.0 等价旧行为）
		1.0,  // shortLev
	)
	if got {
		t.Error("3 仓活 + 新仓 $760 → venueShort $3040 > $3000 cap, want false (skip)")
	}
}

// TestSwapArbVenueExposureCheck_WithinCap ② cap 内 → true（ok）。
// 同 3 仓但 equity $5000 × cap 2.0 = $10000 限额；$2280 + $760 = $3040 ≤ $10000 → ok。
func TestSwapArbVenueExposureCheck_WithinCap(t *testing.T) {
	got := swapArbVenueExposureCheck(
		threeSwapArbLegs(),
		canonicalByPosStub,
		mockUSDPerLotForLeg(76000),
		"529:mt5", "277:mt5",
		760,  // newNotional
		5000, // eqLong
		5000, // eqShort
		2.0,  // cap
		1.0,  // longLev
		1.0,  // shortLev
	)
	if !got {
		t.Error("3 仓活 + 新仓 $760 → venueShort $3040 ≤ $10000 cap, want true (ok)")
	}
}

// TestSwapArbVenueExposureCheck_LeveragePass D-174：1:1000 杠杆下大名义通过。
// exposure $10000 / 1000 = $10 保证金 ≤ eq $1500 × cap 2.0 = $3000 → ok。
func TestSwapArbVenueExposureCheck_LeveragePass(t *testing.T) {
	got := swapArbVenueExposureCheck(
		threeSwapArbLegs(),
		canonicalByPosStub,
		mockUSDPerLotForLeg(76000),
		"529:mt5", "277:mt5",
		760,    // newNotional
		1500,   // eqLong
		1500,   // eqShort
		2.0,    // cap
		1000.0, // longLev（1:1000）
		1000.0, // shortLev
	)
	if !got {
		t.Error("1:1000 杠杆：venueShort $3040/1000=$3.04 ≤ $3000 cap, want true (ok)")
	}
}

// TestSwapArbVenueExposureOK_VenueOffline_FailClosed ③ venue 离线 → false（fail-closed）。
// Service 仅接 acctMon（store=nil），short venue 离线 → AccountEquityUSD online=false → false。
func TestSwapArbVenueExposureOK_VenueOffline_FailClosed(t *testing.T) {
	longQ := &fakeAccountQuerier{key: "529:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "529:mt5", Venue: "529", VenueType: "mt5", AssetType: "cfd",
		Balance: 5000, Equity: 5000, Currency: "USD",
	}}
	shortQ := &fakeAccountQuerier{key: "277:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "277:mt5", Venue: "277", VenueType: "mt5", AssetType: "cfd",
		Balance: 5000, Equity: 5000, Currency: "USD",
	}}
	s := newSwapArbServiceWithAccts(t, longQ, shortQ)
	s.swapArbVenueExposureCap = 2.0

	// baseline：两 venue 在线 → cap=2.0/equity=5000/无 store → 放行 true。
	if !s.SwapArbVenueExposureOK(context.Background(), "529:mt5", "277:mt5", 760) {
		t.Fatal("baseline: both online + store=nil → want true, got false")
	}

	// short venue 离线 → fail-closed false。
	shortQ.err = context.DeadlineExceeded
	s.acctMon.Refresh(context.Background())
	if s.SwapArbVenueExposureOK(context.Background(), "529:mt5", "277:mt5", 760) {
		t.Error("venue offline: want false (fail-closed), got true")
	}
}

// TestSwapArbVenueExposureCheck_CapZero_NotEnabled ④ cap=0 → true（不启用，等价 bareCap≤0 先例）。
func TestSwapArbVenueExposureCheck_CapZero_NotEnabled(t *testing.T) {
	got := swapArbVenueExposureCheck(
		threeSwapArbLegs(),
		canonicalByPosStub,
		mockUSDPerLotForLeg(76000),
		"529:mt5", "277:mt5",
		760,  // newNotional
		1500, // eqLong
		1500, // eqShort
		0,    // cap=0 → 不启用
		1.0,  // longLev
		1.0,  // shortLev
	)
	if !got {
		t.Error("cap=0 → 不启用, want true, got false")
	}
}

// TestCanonicalFromComboKey 从组合键/opportunity_id 派生 canonical。
func TestCanonicalFromComboKey(t *testing.T) {
	cases := []struct {
		oppID string
		want  string
	}{
		{"SWAPARB:AUDJPY:529:mt5:277:mt5", "AUDJPY"},               // 新格式 6 段
		{"SWAPARB:AUDJPY:529:mt5:277:mt5:1695000000000", "AUDJPY"}, // 旧 tick 7 段（尾段纯数字）
		{"SWAPARB:BTCUSDT:okx:perp:binance:perp", "BTCUSDT"},       // CEX perp
		{"", ""},        // 空串
		{"SWAPARB", ""}, // 单段无 canonical
	}
	for _, c := range cases {
		got := canonicalFromComboKey(c.oppID)
		if got != c.want {
			t.Errorf("canonicalFromComboKey(%q)=%q want %q", c.oppID, got, c.want)
		}
	}
}

// TestSwapArbVenueExposureOK_CexLegBaseQty D-311 R5 缺陷#6b：CEX 腿持久化 qty=基础币，
// 敞口=qty×price×fx，勿乘 ct_val——okx ct_val=1000 腿旧码敞口放千倍，新 SWAPARB 被静默过度拦截。
func TestSwapArbVenueExposureOK_CexLegBaseQty(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	for _, tbl := range []string{"position_legs", "positions"} {
		if _, err := st.Pool().Exec(ctx, "TRUNCATE "+tbl+" CASCADE"); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	var posID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ('SWAPARB:TRXUSDT:okx:perp:binance:perp', 'SWAPARB', 'OPEN', NOW()) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	// okx TRX-USDT-SWAP ct_val=1000：2950 基础币 ≈ 2.95 张，qty 存基础币。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
		 VALUES ($1, 1, 'OPEN', 'okx','perp','TRX-USDT-SWAP', 'BUY', 2950, 0.10, 'FILLED')`, posID); err != nil {
		t.Fatalf("insert leg: %v", err)
	}

	longQ := &fakeAccountQuerier{key: "okx:perp", acct: &reconcile.AccountInfo{
		VenueKey: "okx:perp", Venue: "okx", VenueType: "perp", AssetType: "cex",
		Balance: 500, Equity: 500, Currency: "USD",
	}}
	shortQ := &fakeAccountQuerier{key: "binance:perp", acct: &reconcile.AccountInfo{
		VenueKey: "binance:perp", Venue: "binance", VenueType: "perp", AssetType: "cex",
		Balance: 500, Equity: 500, Currency: "USD",
	}}
	s := newSwapArbServiceWithAccts(t, longQ, shortQ)
	s.store = st
	s.swapArbVenueExposureCap = 2.0
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "TRXUSDT", Venue: "okx", VenueType: "perp", Bid: 0.10, Ask: 0.10, Time: time.Now(), VenueSymbol: "TRX-USDT-SWAP"})
	s.qbus = qb

	// 存量敞口=2950 基础币×$0.10=$295；新仓 $100 → okx 侧 $395 ≤ 500×2.0=$1000 → 放行。
	// 旧码 usdPerLot=0.10×ct_val1000=$100：敞口 $295,000 → 误拒。
	if !s.SwapArbVenueExposureOK(ctx, "okx:perp", "binance:perp", 100) {
		t.Error("CEX 腿敞口应按 qty(基础币)×price 计（$295+$100 ≤ $1000），want true got false")
	}
}

// TestSwapArbVenueExposureOK_CexLegFilledQty D-311 R6：新时代 CEX 行 qty=1012 合约数、
// filled_qty=10.12 基础币 → 敞口按 EffectiveQty=10.12×price，勿按 qty=1012 放大百倍。
func TestSwapArbVenueExposureOK_CexLegFilledQty(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	for _, tbl := range []string{"position_legs", "positions"} {
		if _, err := st.Pool().Exec(ctx, "TRUNCATE "+tbl+" CASCADE"); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	var posID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ('SWAPARB:CLUSDT:gate:perp:binance:perp', 'SWAPARB', 'OPEN', NOW()) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	// 新时代行：qty=1012 张（LegSpec 请求合约数），filled_qty=10.12 成交基础币。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, filled_qty, price_expected, status)
		 VALUES ($1, 1, 'OPEN', 'gate','perp','CL_USDT', 'BUY', 1012, 10.12, 63.5, 'FILLED')`, posID); err != nil {
		t.Fatalf("insert leg: %v", err)
	}

	longQ := &fakeAccountQuerier{key: "gate:perp", acct: &reconcile.AccountInfo{
		VenueKey: "gate:perp", Venue: "gate", VenueType: "perp", AssetType: "cex",
		Balance: 500, Equity: 500, Currency: "USD",
	}}
	shortQ := &fakeAccountQuerier{key: "binance:perp", acct: &reconcile.AccountInfo{
		VenueKey: "binance:perp", Venue: "binance", VenueType: "perp", AssetType: "cex",
		Balance: 500, Equity: 500, Currency: "USD",
	}}
	s := newSwapArbServiceWithAccts(t, longQ, shortQ)
	s.store = st
	s.swapArbVenueExposureCap = 2.0
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "CLUSDT", Venue: "gate", VenueType: "perp", Bid: 63.5, Ask: 63.5, Time: time.Now(), VenueSymbol: "CL_USDT"})
	s.qbus = qb

	// 正确敞口=10.12 基础币×$63.5=$642.62；+新仓 $100=$742.62 ≤ 500×2.0=$1000 → 放行。
	// 旧码 qty=1012×$63.5=$64,262 → 误拒。
	if !s.SwapArbVenueExposureOK(ctx, "gate:perp", "binance:perp", 100) {
		t.Error("CEX 新行敞口应按 EffectiveQty(基础币)×price 计（$642.62+$100 ≤ $1000），want true got false")
	}
}
