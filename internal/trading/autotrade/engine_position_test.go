package autotrade

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// seedSwapPosition 建一条 SWAPARB 持仓 + 双腿（BUY vA:mt5 / SELL vB:mt5），oppID canonical=AUDJPY。
// recalcNetSwap/recalcNetFunding fail-closed 单测共用。
func seedSwapPosition(t *testing.T, s *pgstore.Store) *pgstore.Position {
	t.Helper()
	ctx := context.Background()
	pos := &pgstore.Position{
		OpportunityID: "SWAPARB:AUDJPY:vA:vB:1700000000000",
		Type:          "SWAPARB",
		Status:        "OPEN",
	}
	id, err := s.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("create position: %v", err)
	}
	pos.ID = id
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 1, Phase: "OPEN", Venue: "vA:mt5", Symbol: "AUDJPY",
		Side: "BUY", Qty: 1.0, Status: "FILLED",
	}); err != nil {
		t.Fatalf("create leg1: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 2, Phase: "OPEN", Venue: "vB:mt5", Symbol: "AUDJPY",
		Side: "SELL", Qty: 1.0, Status: "FILLED",
	}); err != nil {
		t.Fatalf("create leg2: %v", err)
	}
	return pos
}

// TestRecalcNetSwap_NormalBothVenues D-105 T1：双 venue 新鲜 fact → 现值回归（true）。
func TestRecalcNetSwap_NormalBothVenues(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	pos := seedSwapPosition(t, s)
	e := &Engine{store: s}
	got, ok := e.recalcNetSwap(ctx, pos)
	if !ok {
		t.Fatalf("normal both venues: want ok=true, got false")
	}
	want := 2.0 // 5.0 + (-3.0)
	if got != want {
		t.Errorf("recalcNetSwap want %.4f, got %.4f", want, got)
	}
}

// TestRecalcNetSwap_MissingVenue D-105 T1：单 venue fact 缺失 → (0,false) 不决策平仓（fail-closed）。
// 修复 #264 误平根因：旧代码静默取 0 → 0+0=0≤0 假信号平仓。
func TestRecalcNetSwap_MissingVenue(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	// 只有 swap_long vA，缺 swap_short vB（模拟测试截断 facts → venue 缺失）。
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vX", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	pos := seedSwapPosition(t, s)
	e := &Engine{store: s}
	got, ok := e.recalcNetSwap(ctx, pos)
	if ok {
		t.Errorf("missing venue: want ok=false (fail-closed, no decision), got ok=true val=%.4f", got)
	}
	if got != 0 {
		t.Errorf("missing venue: want val=0, got %.4f", got)
	}
}

// TestRecalcNetSwap_StaleMatchedFact D-105 T1：匹配到的 fact 陈旧>48h → (0,false)。
// 修复：旧代码查 longFacts[0] 列表首元素（非该 venue 的 fact），匹配后新鲜度未真正检查。
func TestRecalcNetSwap_StaleMatchedFact(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	stale := now.Add(-49 * time.Hour) // >48h
	// vA/vB 匹配的 fact 陈旧；另插一行新鲜但非匹配 venue（vC），验证守卫查的是匹配 fact 不是首元素。
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vC", Symbol: "AUDJPY", Value: 9.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: stale, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: stale, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	pos := seedSwapPosition(t, s)
	e := &Engine{store: s, cfg: gate.Config{SwapFactMaxAge: 2 * time.Hour}} // D-159 W4a R7：TTL 统一用 cfg.SwapFactMaxAge
	got, ok := e.recalcNetSwap(ctx, pos)
	if ok {
		t.Errorf("stale matched fact: want ok=false, got ok=true val=%.4f", got)
	}
}

// --- recalcNetFunding D-312 逐腿 :perp 口径（A1-A7）---

// seedFundingPosition 建一条 PERP_FUNDING 持仓 + 双腿（BUY/SELL venue 可配），oppID canonical=BTCUSDT。
// funding 退出专用 fixture——旧 seedSwapPosition 是 vA:mt5/vB:mt5 腿，venue 类型与
// funding 函数真实消费场景不符（D-312 §1「测试反讽」=bug 存活至今的结构性原因），禁再复用测 funding。
func seedFundingPosition(t *testing.T, s *pgstore.Store, buyVenue, sellVenue string) *pgstore.Position {
	t.Helper()
	ctx := context.Background()
	pos := &pgstore.Position{
		OpportunityID: "PERP_FUNDING:BTCUSDT:vA:vB:1700000000000",
		Type:          "PERP_FUNDING",
		Status:        "OPEN",
	}
	id, err := s.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("create position: %v", err)
	}
	pos.ID = id
	for i, leg := range []struct{ venue, side string }{{buyVenue, "BUY"}, {sellVenue, "SELL"}} {
		if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID: id, LegNo: int16(i + 1), Phase: "OPEN", Venue: leg.venue, Symbol: "BTCUSDT",
			Side: leg.side, Qty: 1.0, Status: "FILLED",
		}); err != nil {
			t.Fatalf("create leg%d: %v", i+1, err)
		}
	}
	return pos
}

// seedFundingFact 插一条 funding fact（Symbol=fixture canonical BTCUSDT，年化百分点）。
func seedFundingFact(t *testing.T, s *pgstore.Store, venue string, value float64, ts time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindFunding, Venue: venue, Symbol: "BTCUSDT", Value: value, Unit: fact.UnitPctAnnualized, Ts: ts, Src: venue},
	}); err != nil {
		t.Fatalf("insert funding fact %s: %v", venue, err)
	}
}

// assertFailClosed A5-A7 共用断言：fail-closed 必须 (0,false)——ok=false 且不带回半截值。
func assertFailClosed(t *testing.T, got float64, ok bool) {
	t.Helper()
	if ok {
		t.Errorf("want ok=false (fail-closed), got ok=true val=%.4f", got)
	}
	if got != 0 {
		t.Errorf("want val=0 on fail-closed, got %.4f", got)
	}
}

// A1 符号回归（B1）：BUY vA:perp(4) + SELL vB:perp(10) → net=+6.0（SELL 收 +10、BUY 付 −4）。
// 旧码 long−short=4−10=−6——把错误符号写进回归的现存锚，先红后绿。
func TestRecalcNetFunding_A1_SignRegression(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	seedFundingFact(t, s, "vA", 4.0, now)
	seedFundingFact(t, s, "vB", 10.0, now)
	pos := seedFundingPosition(t, s, "vA:perp", "vB:perp")
	e := &Engine{store: s}
	got, ok := e.recalcNetFunding(ctx, pos, 0)
	if !ok {
		t.Fatalf("A1: want ok=true, got false")
	}
	want := 6.0
	if got != want {
		t.Errorf("A1 sign: want %.4f, got %.4f", want, got)
	}
}

// A2 反向（B1）：BUY vA:perp(10) + SELL vB:perp(4) → net=−6.0（BUY 付得多=净支出）。
// 旧码 10−4=+6，盈利仓反被判 breach，先红后绿。
func TestRecalcNetFunding_A2_Inverse(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	seedFundingFact(t, s, "vA", 10.0, now)
	seedFundingFact(t, s, "vB", 4.0, now)
	pos := seedFundingPosition(t, s, "vA:perp", "vB:perp")
	e := &Engine{store: s}
	got, ok := e.recalcNetFunding(ctx, pos, 0)
	if !ok {
		t.Fatalf("A2: want ok=true, got false")
	}
	want := -6.0
	if got != want {
		t.Errorf("A2 inverse: want %.4f, got %.4f", want, got)
	}
}

// A3 同所套保（B2）：BUY binance:spot + SELL binance:perp，fact(binance)=8 → net=+8.0。
// spot 腿贡献 0 不查 fact；旧码两腿同 bare 命中同一条 fact → net≡0 ≤0 首查即误平。
func TestRecalcNetFunding_A3_SameVenueHedge(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	seedFundingFact(t, s, "binance", 8.0, now)
	pos := seedFundingPosition(t, s, "binance:spot", "binance:perp")
	e := &Engine{store: s}
	got, ok := e.recalcNetFunding(ctx, pos, 0)
	if !ok {
		t.Fatalf("A3: want ok=true, got false")
	}
	want := 8.0
	if got != want {
		t.Errorf("A3 same-venue hedge: want %.4f, got %.4f", want, got)
	}
}

// A4 跨所套保（B3）：BUY okx:spot + SELL binance:perp，仅 binance fact=8 → net=+8.0。
// spot 腿不取数——okx 无 fact 也不 fail；旧码 buyBare=okx 缺 fact → (0,false) 永不退出。
func TestRecalcNetFunding_A4_CrossVenueHedge(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	seedFundingFact(t, s, "binance", 8.0, now)
	pos := seedFundingPosition(t, s, "okx:spot", "binance:perp")
	e := &Engine{store: s}
	got, ok := e.recalcNetFunding(ctx, pos, 0)
	if !ok {
		t.Fatalf("A4: want ok=true, got false")
	}
	want := 8.0
	if got != want {
		t.Errorf("A4 cross-venue hedge: want %.4f, got %.4f", want, got)
	}
}

// A5（B4）：perp 腿缺 fact → (0,false) fail-closed（承 D-105 T1 MissingVenue 语义，换 :perp 腿）。
func TestRecalcNetFunding_A5_MissingFact(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	seedFundingFact(t, s, "vA", 4.0, now) // 只插 vA，SELL vB:perp 缺 fact
	pos := seedFundingPosition(t, s, "vA:perp", "vB:perp")
	e := &Engine{store: s}
	got, ok := e.recalcNetFunding(ctx, pos, 0)
	assertFailClosed(t, got, ok)
}

// A6（B4）：fact 陈旧 >SwapFactMaxAge → (0,false)（承 D-105 T1 StaleMatchedFact 语义）。
func TestRecalcNetFunding_A6_StaleFact(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	stale := now.Add(-3 * time.Hour) // >2h
	seedFundingFact(t, s, "vA", 4.0, stale)
	seedFundingFact(t, s, "vB", 10.0, stale)
	pos := seedFundingPosition(t, s, "vA:perp", "vB:perp")
	e := &Engine{store: s, cfg: gate.Config{SwapFactMaxAge: 2 * time.Hour}} // D-159 W4a R7：TTL 统一用 cfg.SwapFactMaxAge
	got, ok := e.recalcNetFunding(ctx, pos, 0)
	assertFailClosed(t, got, ok)
}

// A7（B4）：无 :perp 腿 → (0,false)（防「全 spot 仓 net=0 ≤0 误平」；两腿 :spot 各有新鲜 fact
// 也不得求值）。旧码按 bare venue 匹配 fact 求值 ok=true val=−6，先红后绿。
func TestRecalcNetFunding_A7_NoPerpLeg(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	seedFundingFact(t, s, "vA", 4.0, now)
	seedFundingFact(t, s, "vB", 10.0, now)
	pos := seedFundingPosition(t, s, "vA:spot", "vB:spot")
	e := &Engine{store: s}
	got, ok := e.recalcNetFunding(ctx, pos, 0)
	assertFailClosed(t, got, ok)
}
