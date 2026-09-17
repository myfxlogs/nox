package swapledger

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// stubTripleDay 测试用 TripleDayResolver（D-077 修复⑪A M3：per-venue + per-symbol）。
type stubTripleDay struct {
	venueDays  map[string]time.Weekday // key = venue
	symbolDays map[string]time.Weekday // key = "venue:canonical"
}

func (s stubTripleDay) TripleSwapDay(venue, canonical string) (time.Weekday, bool) {
	// 优先 symbol 级
	if s.symbolDays != nil {
		if d, ok := s.symbolDays[venue+":"+canonical]; ok {
			return d, true
		}
	}
	// 回退 venue 级
	if d, ok := s.venueDays[venue]; ok {
		return d, true
	}
	return 0, false
}

// stubNotional 测试用 NotionalResolver。
type stubNotional struct {
	vals map[string]float64 // key = canonical+":"+venueKey+":"+side
}

func (s stubNotional) USDPerLotForLeg(canonical, venueKey, side string) (float64, bool) {
	v, ok := s.vals[canonical+":"+venueKey+":"+side]
	return v, ok
}

// TestSettleMultiplier_NormalDay 非三倍日 → 1。
func TestSettleMultiplier_NormalDay(t *testing.T) {
	r := stubTripleDay{venueDays: map[string]time.Weekday{"vA": time.Wednesday}}
	// Monday = 非三倍日
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC) // 2026-08-24 is Monday
	got := settleMultiplier("vA:mt5", "AUDJPY", monday, r)
	if got != 1 {
		t.Errorf("normal day multiplier want 1, got %v", got)
	}
}

// TestSettleMultiplier_TripleDay 三倍日 → 3。
func TestSettleMultiplier_TripleDay(t *testing.T) {
	r := stubTripleDay{venueDays: map[string]time.Weekday{"vA": time.Wednesday}}
	// Wednesday = 三倍日
	wed := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC) // 2026-08-26 is Wednesday
	got := settleMultiplier("vA:mt5", "AUDJPY", wed, r)
	if got != 3 {
		t.Errorf("triple day multiplier want 3, got %v", got)
	}
}

// TestSettleMultiplier_UnknownVenueFailClosed venue 未知 → fail-closed 返回 0（D-077 修复⑪A M4：删周三回退）。
func TestSettleMultiplier_UnknownVenueFailClosed(t *testing.T) {
	r := stubTripleDay{venueDays: map[string]time.Weekday{}} // 空 = 全未知
	wed := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)      // Wednesday
	got := settleMultiplier("vX:mt5", "AUDJPY", wed, r)
	if got != 0 {
		t.Errorf("unknown venue fail-closed want 0 (no Wednesday fallback), got %v", got)
	}
	mon := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC) // Monday
	got = settleMultiplier("vX:mt5", "AUDJPY", mon, r)
	if got != 0 {
		t.Errorf("unknown venue fail-closed non-Wednesday want 0, got %v", got)
	}
}

// TestSettleMultiplier_PerVenueDiff 各 venue 三倍日不同（archive 实证：4 家不全周三）。
func TestSettleMultiplier_PerVenueDiff(t *testing.T) {
	r := stubTripleDay{venueDays: map[string]time.Weekday{
		"vA": time.Wednesday,
		"vB": time.Tuesday,
	}}
	tue := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC) // Tuesday
	// vA 三倍日=周三 → 周二=1
	if got := settleMultiplier("vA:mt5", "AUDJPY", tue, r); got != 1 {
		t.Errorf("vA Tuesday multiplier want 1, got %v", got)
	}
	// vB 三倍日=周二 → 周二=3
	if got := settleMultiplier("vB:mt5", "AUDJPY", tue, r); got != 3 {
		t.Errorf("vB Tuesday multiplier want 3, got %v", got)
	}
}

// TestLegSwapAmount_BuyLegSwapLong BUY 腿用 swap_long rate。
func TestLegSwapAmount_BuyLegSwapLong(t *testing.T) {
	// D-159 W3a R5：legSwapAmount 接收 map[factKey]fact.Fact（含 Ts 供 TTL 检查）。
	longMap := map[factKey]fact.Fact{factKey{"AUDJPY", "vA"}: {Value: 5.0, Ts: time.Now()}} // 5 bps/日
	shortMap := map[factKey]fact.Fact{}
	fundMap := map[factKey]fact.Fact{}
	notional := stubNotional{vals: map[string]float64{"AUDJPY:vA:mt5:ask": 100000}} // 1 手=10万 USD
	r := stubTripleDay{venueDays: map[string]time.Weekday{"vA": time.Wednesday}}
	l := &Ledger{notional: notional, tripleDay: r}

	leg := pgstore.PositionLeg{Venue: "vA:mt5", Side: "BUY", Qty: 2.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC) // Monday → multiplier=1
	got, ok, _ := l.legSwapAmount("AUDJPY", leg, monday, false, longMap, shortMap, fundMap)
	if !ok {
		t.Fatal("want ok=true, got false")
	}
	// amount = 5/10000 × (100000×2) × 1 = 100 USD
	want := 5.0 / 10000 * 100000 * 2 * 1
	if math.Abs(got-want) > 0.01 {
		t.Errorf("buy leg swap amount want %.2f, got %.2f", want, got)
	}
}

// TestLegSwapAmount_SellLegSwapShort SELL 腿用 swap_short rate。
func TestLegSwapAmount_SellLegSwapShort(t *testing.T) {
	longMap := map[factKey]fact.Fact{}
	shortMap := map[factKey]fact.Fact{factKey{"AUDJPY", "vB"}: {Value: -3.0, Ts: time.Now()}} // -3 bps/日（付 swap）
	fundMap := map[factKey]fact.Fact{}
	notional := stubNotional{vals: map[string]float64{"AUDJPY:vB:mt5:bid": 100000}}
	r := stubTripleDay{venueDays: map[string]time.Weekday{"vB": time.Wednesday}}
	l := &Ledger{notional: notional, tripleDay: r}

	leg := pgstore.PositionLeg{Venue: "vB:mt5", Side: "SELL", Qty: 1.0}
	wed := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC) // Wednesday → multiplier=3
	got, ok, _ := l.legSwapAmount("AUDJPY", leg, wed, false, longMap, shortMap, fundMap)
	if !ok {
		t.Fatal("want ok=true, got false")
	}
	// amount = -3/10000 × (100000×1) × 3 = -90 USD（三倍日付 90）
	want := -3.0 / 10000 * 100000 * 1 * 3
	if math.Abs(got-want) > 0.01 {
		t.Errorf("sell leg swap amount want %.2f, got %.2f", want, got)
	}
}

// TestLegSwapAmount_FundingType funding 类用年化百分点（D-077 修复⑪A M2：删三倍虚乘）。
// D-322：fixture 复刻历史真实形态（funding 腿=空 perp 收正费率，SELL×正率→正=收）；
// 腿向符号四象限见 TestLegSwapAmount_FundingDirectionQuadrants。
func TestLegSwapAmount_FundingType(t *testing.T) {
	longMap := map[factKey]fact.Fact{}
	shortMap := map[factKey]fact.Fact{}
	fundMap := map[factKey]fact.Fact{factKey{"BTCUSDT", "binance"}: {Value: 10.95, Ts: time.Now()}} // 10.95% 年化
	notional := stubNotional{vals: map[string]float64{"BTCUSDT:binance:spot:bid": 50000}}
	r := stubTripleDay{}
	l := &Ledger{notional: notional, tripleDay: r}

	leg := pgstore.PositionLeg{Venue: "binance:spot", Side: "SELL", Qty: 1.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	got, ok, _ := l.legSwapAmount("BTCUSDT", leg, monday, true, longMap, shortMap, fundMap)
	if !ok {
		t.Fatal("want ok=true, got false")
	}
	// D-077 修复⑪A M2：amount = 10.95/100/365 × 50000 = 15 USD（日 funding，含 8h×3 周期合计）
	// 旧公式 × 3 = 45 USD（3 倍虚乘）→ 删除
	want := 10.95 / 100 / DaysPerYear * 50000
	if math.Abs(got-want) > 0.01 {
		t.Errorf("funding amount want %.2f (no triple multiply), got %.2f", want, got)
	}
}

// TestLegSwapAmount_FundingDirectionQuadrants D-322 S2：funding 腿方向四象限。
// 符号语义从规格推导（资金费=多付空收：rate>0 时 long 付/short 收），非归纳现行码——
// BUY×负率→正（收，pos 485 形态：多 perp 收负费率）；BUY×正率→负（付）；
// SELL×正率→正（收，PERP_FUNDING 历史形态）；SELL×负率→负（付）。
// 摘除 S1 符号修正 → BUY 两象限转红（变异对抗锚）。
func TestLegSwapAmount_FundingDirectionQuadrants(t *testing.T) {
	notional := stubNotional{vals: map[string]float64{
		"CLUSDT:gate:perp:ask": 100000, // BUY→ask
		"CLUSDT:gate:perp:bid": 100000, // SELL→bid
	}}
	l := &Ledger{notional: notional, tripleDay: stubTripleDay{}}
	cases := []struct {
		name string
		side string
		rate float64
		want float64
	}{
		{"BUY×负率→收", "BUY", -7.3, 7.3 / 100 / DaysPerYear * 100000},
		{"BUY×正率→付", "BUY", 10.95, -10.95 / 100 / DaysPerYear * 100000},
		{"SELL×正率→收", "SELL", 10.95, 10.95 / 100 / DaysPerYear * 100000},
		{"SELL×负率→付", "SELL", -7.3, -(7.3 / 100 / DaysPerYear * 100000)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fundMap := map[factKey]fact.Fact{factKey{"CLUSDT", "gate"}: {Value: c.rate, Ts: time.Now()}}
			leg := pgstore.PositionLeg{Venue: "gate:perp", Side: c.side, Qty: 1.0}
			monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
			got, ok, _ := l.legSwapAmount("CLUSDT", leg, monday, true, map[factKey]fact.Fact{}, map[factKey]fact.Fact{}, fundMap)
			if !ok {
				t.Fatal("want ok=true, got false")
			}
			if math.Abs(got-c.want) > 0.01 {
				t.Errorf("%s rate=%.2f: amount=%.4f, want %.4f", c.side, c.rate, got, c.want)
			}
		})
	}
}

// TestLegSwapAmount_RateNotFound swap rate 缺失 → fail-closed ok=false。
func TestLegSwapAmount_RateNotFound(t *testing.T) {
	notional := stubNotional{vals: map[string]float64{"AUDJPY:vA:mt5:ask": 100000}}
	r := stubTripleDay{}
	l := &Ledger{notional: notional, tripleDay: r}

	leg := pgstore.PositionLeg{Venue: "vA:mt5", Side: "BUY", Qty: 1.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	_, ok, _ := l.legSwapAmount("AUDJPY", leg, monday, false, map[factKey]fact.Fact{}, map[factKey]fact.Fact{}, map[factKey]fact.Fact{})
	if ok {
		t.Error("rate not found want ok=false, got true")
	}
}

// TestLegSwapAmount_NotionalNotFound notional 缺失 → fail-closed ok=false。
func TestLegSwapAmount_NotionalNotFound(t *testing.T) {
	longMap := map[factKey]fact.Fact{factKey{"AUDJPY", "vA"}: {Value: 5.0, Ts: time.Now()}}
	notional := stubNotional{vals: map[string]float64{}} // 空 = notional 不可用
	r := stubTripleDay{}
	l := &Ledger{notional: notional, tripleDay: r}

	leg := pgstore.PositionLeg{Venue: "vA:mt5", Side: "BUY", Qty: 1.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	_, ok, _ := l.legSwapAmount("AUDJPY", leg, monday, false, longMap, map[factKey]fact.Fact{}, map[factKey]fact.Fact{})
	if ok {
		t.Error("notional not found want ok=false, got true")
	}
}

// TestLegSwapAmount_ZeroRate 零息 venue（rate=0）→ amount=0（不写 cashflow）。
func TestLegSwapAmount_ZeroRate(t *testing.T) {
	longMap := map[factKey]fact.Fact{factKey{"AUDJPY", "vA"}: {Value: 0.0, Ts: time.Now()}} // 零息
	notional := stubNotional{vals: map[string]float64{"AUDJPY:vA:mt5:ask": 100000}}
	r := stubTripleDay{venueDays: map[string]time.Weekday{"vA": time.Wednesday}}
	l := &Ledger{notional: notional, tripleDay: r}

	leg := pgstore.PositionLeg{Venue: "vA:mt5", Side: "BUY", Qty: 1.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	got, ok, _ := l.legSwapAmount("AUDJPY", leg, monday, false, longMap, map[factKey]fact.Fact{}, map[factKey]fact.Fact{})
	if !ok {
		t.Fatal("zero rate want ok=true, got false")
	}
	if got != 0 {
		t.Errorf("zero rate amount want 0, got %v", got)
	}
}

// TestVenueFromKey venueKey 格式解析。
func TestVenueFromKey(t *testing.T) {
	tests := []struct {
		key, want string
	}{
		{"vA:mt5", "vA"},
		{"binance:spot", "binance"},
		{"ic", "ic"}, // 无冒号 → 原样
		{"", ""},
	}
	for _, tt := range tests {
		if got := venueFromKey(tt.key); got != tt.want {
			t.Errorf("venueFromKey(%q) want %q, got %q", tt.key, tt.want, got)
		}
	}
}

// TestCanonicalFromOppID opportunity_id 格式解析。
func TestCanonicalFromOppID(t *testing.T) {
	tests := []struct {
		oppID, want string
	}{
		{"SWAPARB:AUDJPY:vA:mt5:vB:mt5:123", "AUDJPY"},
		{"CARRY:BTCUSDT:binance:spot:okx:spot:456", "BTCUSDT"},
		{"X", ""}, // 无冒号 → 空
		{"", ""},  // 空 → 空
	}
	for _, tt := range tests {
		if got := canonicalFromOppID(tt.oppID); got != tt.want {
			t.Errorf("canonicalFromOppID(%q) want %q, got %q", tt.oppID, tt.want, got)
		}
	}
}

// TestCashflowKind 已迁 ledger_helpers_test.go（D-314 S6 方案 A：cashflowKind 改两参数签名，
// 新同名测试含 XASSET_CARRY per-leg + 向后兼容断言）。

// TestIsFundingType funding 类型判定。
func TestIsFundingType(t *testing.T) {
	if !isFundingType("FUNDING_HEDGE") {
		t.Error("FUNDING_HEDGE want true")
	}
	if !isFundingType("PERP_FUNDING") {
		t.Error("PERP_FUNDING want true")
	}
	if isFundingType("SWAPARB") {
		t.Error("SWAPARB want false")
	}
	if isFundingType("CARRY") {
		t.Error("CARRY want false")
	}
}

// TestSettleMultiplier_WeekendNoTriple D-110：tripleDay=Sunday/Saturday 时 multiplier=1（周末不开盘）。
func TestSettleMultiplier_WeekendNoTriple(t *testing.T) {
	tests := []struct {
		name      string
		tripleDay time.Weekday
		day       time.Time
		want      float64
	}{
		{"Sunday tripleDay + Sunday → 1（不开盘）", time.Sunday, time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC), 1},
		{"Sunday tripleDay + Wednesday → 1（非三倍日）", time.Sunday, time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC), 1},
		{"Saturday tripleDay + Saturday → 1（不开盘）", time.Saturday, time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC), 1},
		{"Wednesday tripleDay + Wednesday → 3（正常三倍日）", time.Wednesday, time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC), 3},
		{"Wednesday tripleDay + Monday → 1（非三倍日）", time.Wednesday, time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := stubTripleDay{venueDays: map[string]time.Weekday{"vA": tt.tripleDay}}
			got := settleMultiplier("vA:mt5", "AUDJPY", tt.day, r)
			if got != tt.want {
				t.Errorf("settleMultiplier want %.0f, got %.0f", tt.want, got)
			}
		})
	}
}

// TestSettleMultiplier_UnknownFailClosed D-110：未知三倍日仍 fail-closed（返回 0）。
func TestSettleMultiplier_UnknownFailClosed(t *testing.T) {
	r := stubTripleDay{} // 无 venueDays → ok=false
	got := settleMultiplier("vX:mt5", "AUDJPY", time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC), r)
	if got != 0 {
		t.Errorf("unknown triple day want 0 (fail-closed), got %.0f", got)
	}
}

// TestLegSwapAmount_NonSettleVenue D-112：venue 在 nonSettleVenues → 返回 (0, true)。
// 即使 rate/notional 可用，不结算 venue 仍记 0（amount=0 → accountPosition 跳过不写 cashflow）。
func TestLegSwapAmount_NonSettleVenue(t *testing.T) {
	shortMap := map[factKey]fact.Fact{factKey{"AUDJPY", "277842155"}: {Value: -1.3, Ts: time.Now()}} // Exness 规格值（SELL 腿用 swap_short）
	notional := stubNotional{vals: map[string]float64{"AUDJPY:277842155:mt5:bid": 75000}}
	r := stubTripleDay{venueDays: map[string]time.Weekday{"277842155": time.Wednesday}}
	l := &Ledger{
		notional:        notional,
		tripleDay:       r,
		nonSettleVenues: map[string]bool{"277842155:mt5": true},
	}

	leg := pgstore.PositionLeg{Venue: "277842155:mt5", Side: "SELL", Qty: 1.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	got, ok, reason := l.legSwapAmount("AUDJPY", leg, monday, false, map[factKey]fact.Fact{}, shortMap, map[factKey]fact.Fact{})
	if !ok {
		t.Fatal("non-settle venue want ok=true, got false")
	}
	if got != 0 {
		t.Errorf("non-settle venue amount want 0, got %v", got)
	}
	// D-193-A 扩包返修 R5：nonSettle 专属 reason（与真零息区分）
	if reason != "non_settle_venue" {
		t.Errorf("non-settle venue reason want \"non_settle_venue\", got %q", reason)
	}
}

// nonSettleStubStore TestAccountPosition_NonSettleVenue 用 mock Store（D-112）。
// D-159 W3a R5：per-leg 幂等——HasCashflowForLegDay 替代 HasCashflowForDay。
// D-193-A 扩包（决策 8）：InsertCashflow → InsertCashflowFull（接收 *pgstore.Cashflow）。
type nonSettleStubStore struct {
	legs            []pgstore.PositionLeg
	hasCashflowLegs map[int16]bool      // legNo → 当日已记（per-leg 幂等模拟）
	insertedVenues  []string            // 记录 InsertCashflowFull 被调用的 venue
	insertedCFs     []*pgstore.Cashflow // D-193-A 扩包：记录完整 Cashflow 供断言
	hasQueries      []string            // D-314：记录 HasCashflowForLegDay 收到的 "legNo:kind"（per-leg 幂等路由断言）
}

func (s *nonSettleStubStore) ListOpenPositionsByType(_ context.Context, _ []string) ([]pgstore.Position, error) {
	return nil, nil
}
func (s *nonSettleStubStore) ListPositionLegs(_ context.Context, _ int64) ([]pgstore.PositionLeg, error) {
	return s.legs, nil
}
func (s *nonSettleStubStore) HasCashflowForLegDay(_ context.Context, _ int64, legNo int16, kind string, _ time.Time) (bool, error) {
	s.hasQueries = append(s.hasQueries, fmt.Sprintf("%d:%s", legNo, kind))
	return s.hasCashflowLegs[legNo], nil
}
func (s *nonSettleStubStore) InsertCashflowFull(_ context.Context, c *pgstore.Cashflow) (bool, error) {
	if s.hasCashflowLegs == nil {
		s.hasCashflowLegs = map[int16]bool{}
	}
	legNo := int16(-1)
	if c.LegNo != nil {
		legNo = *c.LegNo
	}
	s.hasCashflowLegs[legNo] = true
	s.insertedVenues = append(s.insertedVenues, c.Venue)
	s.insertedCFs = append(s.insertedCFs, c)
	return true, nil
}
func (s *nonSettleStubStore) LatestFacts(_ context.Context, _, _, _ string) ([]fact.Fact, error) {
	return nil, nil
}
func (s *nonSettleStubStore) InsertAlert(_ context.Context, _ *int64, _, _ string) error { return nil }

// TestAccountPosition_NonSettleVenue D-112：Exness 腿不写 cashflow（amount=0→跳过），IC 腿正常写。
func TestAccountPosition_NonSettleVenue(t *testing.T) {
	// D-159 W3a R5：map 类型改为 map[factKey]fact.Fact（含 Ts）。
	longMap := map[factKey]fact.Fact{factKey{"AUDJPY", "52993526"}: {Value: 0.5, Ts: time.Now()}}
	shortMap := map[factKey]fact.Fact{factKey{"AUDJPY", "277842155"}: {Value: -1.3, Ts: time.Now()}}
	notional := stubNotional{vals: map[string]float64{
		"AUDJPY:52993526:mt5:ask":  75000,
		"AUDJPY:277842155:mt5:bid": 75000,
	}}
	r := stubTripleDay{venueDays: map[string]time.Weekday{"52993526": time.Wednesday, "277842155": time.Wednesday}}
	store := &nonSettleStubStore{
		hasCashflowLegs: map[int16]bool{}, // 全部未记
		legs: []pgstore.PositionLeg{
			{LegNo: 1, Phase: "OPEN", Venue: "52993526:mt5", Side: "BUY", Qty: 1.0},
			{LegNo: 2, Phase: "OPEN", Venue: "277842155:mt5", Side: "SELL", Qty: 1.0},
		},
	}
	l := &Ledger{
		store:           store,
		notional:        notional,
		tripleDay:       r,
		nonSettleVenues: map[string]bool{"277842155:mt5": true},
	}

	pos := pgstore.Position{ID: 1, Type: "SWAPARB", OpportunityID: "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5:1"}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	n, err := l.accountPosition(context.Background(), pos, monday, longMap, shortMap, map[factKey]fact.Fact{})
	if err != nil {
		t.Fatalf("accountPosition: %v", err)
	}
	// 只 IC 腿写 cashflow（Exness 腿 amount=0 跳过）
	if n != 1 {
		t.Errorf("written = %d, want 1 (only IC leg, Exness skipped)", n)
	}
	if len(store.insertedVenues) != 1 || store.insertedVenues[0] != "52993526:mt5" {
		t.Errorf("inserted venues = %v, want [52993526:mt5] (Exness should be skipped)", store.insertedVenues)
	}
	// D-193-A 扩包 S1 断言（决策 1/3/4/6）：InsertCashflowFull 填事实身份字段。
	if len(store.insertedCFs) != 1 {
		t.Fatalf("insertedCFs len=%d, want 1", len(store.insertedCFs))
	}
	cf := store.insertedCFs[0]
	// 决策 1：event_key 格式 swapledger:<posID>:<legNo>:<kind>:<YYYY-MM-DD>
	wantKey := "swapledger:1:1:swap:2026-08-24"
	if cf.EventKey == nil || *cf.EventKey != wantKey {
		t.Errorf("event_key=%v, want %s", cf.EventKey, wantKey)
	}
	// 决策 3：quality='estimated'（公式路径）
	if cf.Quality == nil || *cf.Quality != "estimated" {
		t.Errorf("quality=%v, want estimated", cf.Quality)
	}
	// 决策 4：source='swapledger'
	if cf.Source == nil || *cf.Source != "swapledger" {
		t.Errorf("source=%v, want swapledger", cf.Source)
	}
	// 决策 6：account_id=venue 前缀数字（52993526）
	if cf.AccountID == nil || *cf.AccountID != 52993526 {
		t.Errorf("account_id=%v, want 52993526", cf.AccountID)
	}
	// 决策 2/7：external_event_id/raw_*=NULL（公式路径不造假）
	if cf.ExternalEventID != nil {
		t.Errorf("external_event_id=%v, want nil (公式路径不造假)", cf.ExternalEventID)
	}
	// 决策 5：occurred_at=day 零点
	if cf.OccurredAt == nil || !cf.OccurredAt.Equal(monday) {
		t.Errorf("occurred_at=%v, want %v (day 零点)", cf.OccurredAt, monday)
	}
}

// TestPerLegIdempotent D-159 W3a R5 A7：per-leg 幂等——leg0 当日已记→跳过 leg0，leg1 未记→补写 leg1。
// 旧 position 级 HasCashflowForDay 会让整笔跳过（一腿已记→整仓跳过→失败腿永久漏记）。
// 新 per-leg HasCashflowForLegDay 只跳过已记腿，未记腿下一轮补写。
func TestPerLegIdempotent(t *testing.T) {
	longMap := map[factKey]fact.Fact{factKey{"AUDJPY", "vA"}: {Value: 5.0, Ts: time.Now()}}
	shortMap := map[factKey]fact.Fact{factKey{"AUDJPY", "vB"}: {Value: -3.0, Ts: time.Now()}}
	notional := stubNotional{vals: map[string]float64{
		"AUDJPY:vA:mt5:ask": 100000,
		"AUDJPY:vB:mt5:bid": 100000,
	}}
	r := stubTripleDay{venueDays: map[string]time.Weekday{"vA": time.Wednesday, "vB": time.Wednesday}}
	// leg0 当日已记（hasCashflowLegs[0]=true），leg1 未记
	store := &nonSettleStubStore{
		hasCashflowLegs: map[int16]bool{0: true},
		legs: []pgstore.PositionLeg{
			{LegNo: 0, Phase: "OPEN", Venue: "vA:mt5", Side: "BUY", Qty: 1.0},
			{LegNo: 1, Phase: "OPEN", Venue: "vB:mt5", Side: "SELL", Qty: 1.0},
		},
	}
	l := &Ledger{store: store, notional: notional, tripleDay: r}

	pos := pgstore.Position{ID: 1, Type: "SWAPARB", OpportunityID: "SWAPARB:AUDJPY:vA:mt5:vB:mt5:1"}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	n, err := l.accountPosition(context.Background(), pos, monday, longMap, shortMap, map[factKey]fact.Fact{})
	if err != nil {
		t.Fatalf("accountPosition: %v", err)
	}
	// 只补写 leg1（leg0 已记跳过），written=1
	if n != 1 {
		t.Errorf("written=%d, want 1 (leg0 skipped, leg1 written)", n)
	}
	// 只 leg1 的 venue 被插入
	if len(store.insertedVenues) != 1 || store.insertedVenues[0] != "vB:mt5" {
		t.Errorf("inserted venues=%v, want [vB:mt5] (leg0 skipped, only leg1 written)", store.insertedVenues)
	}
}

// TestFactExpired D-159 W3a R5 A9 + W4a R7：Fact.Ts 超 factTTL → legSwapAmount fail-closed 返回 (0, false)。
// TTL 由 New 注入（cfg.SwapFactMaxAge env 单源）；测试用 &Ledger{factTTL:24h} 模拟。
func TestFactExpired(t *testing.T) {
	// Fact.Ts 超 24h（25h 前）
	expiredTs := time.Now().Add(-25 * time.Hour)
	longMap := map[factKey]fact.Fact{factKey{"AUDJPY", "vA"}: {Value: 5.0, Ts: expiredTs}}
	notional := stubNotional{vals: map[string]float64{"AUDJPY:vA:mt5:ask": 100000}}
	r := stubTripleDay{venueDays: map[string]time.Weekday{"vA": time.Wednesday}}
	l := &Ledger{notional: notional, tripleDay: r, factTTL: 24 * time.Hour}

	leg := pgstore.PositionLeg{Venue: "vA:mt5", Side: "BUY", Qty: 1.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	got, ok, _ := l.legSwapAmount("AUDJPY", leg, monday, false, longMap, map[factKey]fact.Fact{}, map[factKey]fact.Fact{})
	if ok {
		t.Errorf("expired fact want ok=false (fail-closed), got ok=true amount=%.2f", got)
	}
	if got != 0 {
		t.Errorf("expired fact want amount=0, got %.2f", got)
	}
}

// TestDiagnoseLegSwapAmount_D193A_FactNotFound D-193-A 扩包 S3：
// fact 缺失 → ok=false，Reason 含 "fact not found"。
func TestDiagnoseLegSwapAmount_D193A_FactNotFound(t *testing.T) {
	notional := stubNotional{vals: map[string]float64{"AUDJPY:vA:mt5:ask": 100000}}
	r := stubTripleDay{venueDays: map[string]time.Weekday{"vA": time.Wednesday}}
	l := &Ledger{notional: notional, tripleDay: r}
	pos := pgstore.Position{ID: 1, Type: "SWAPARB", OpportunityID: "SWAPARB:AUDJPY:vA:mt5:vB:mt5:1"}
	leg := pgstore.PositionLeg{LegNo: 1, Phase: "OPEN", Venue: "vB:mt5", Side: "SELL", Qty: 1.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	// longMap 有 vA，shortMap 空（vB 缺失）
	longMap := map[factKey]fact.Fact{factKey{"AUDJPY", "vA"}: {Value: 5.0, Ts: time.Now()}}
	d := l.DiagnoseLegSwapAmount(context.Background(), pos, leg, monday, longMap, map[factKey]fact.Fact{}, map[factKey]fact.Fact{})
	if d.Ok {
		t.Errorf("fact 缺失应 ok=false, got ok=true amount=%.6f", d.Amount)
	}
	if !strings.Contains(d.Reason, "fact not found") {
		t.Errorf("Reason=%q, want contain 'fact not found'", d.Reason)
	}
	if !strings.Contains(d.FactKey, "AUDJPY:vB") {
		t.Errorf("FactKey=%q, want contain 'AUDJPY:vB'", d.FactKey)
	}
}

// TestDiagnoseLegSwapAmount_D193A_Normal D-193-A 扩包 S3：
// fact 命中 + notional 命中 → ok=true, amount≠0, Reason="normal"。
func TestDiagnoseLegSwapAmount_D193A_Normal(t *testing.T) {
	notional := stubNotional{vals: map[string]float64{"AUDJPY:vA:mt5:ask": 100000}}
	r := stubTripleDay{venueDays: map[string]time.Weekday{"vA": time.Wednesday}}
	l := &Ledger{notional: notional, tripleDay: r}
	pos := pgstore.Position{ID: 1, Type: "SWAPARB", OpportunityID: "SWAPARB:AUDJPY:vA:mt5:vB:mt5:1"}
	leg := pgstore.PositionLeg{LegNo: 0, Phase: "OPEN", Venue: "vA:mt5", Side: "BUY", Qty: 1.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC) // Monday
	longMap := map[factKey]fact.Fact{factKey{"AUDJPY", "vA"}: {Value: 5.0, Ts: time.Now()}}
	d := l.DiagnoseLegSwapAmount(context.Background(), pos, leg, monday, longMap, map[factKey]fact.Fact{}, map[factKey]fact.Fact{})
	if !d.Ok {
		t.Errorf("fact+notional 命中应 ok=true, got ok=false Reason=%s", d.Reason)
	}
	if d.Amount == 0 {
		t.Errorf("amount=0, want non-zero (rate=5bps × notional=100000 × mult=1)")
	}
	if d.Reason != "normal" {
		t.Errorf("Reason=%q, want 'normal'", d.Reason)
	}
	if !d.FactHit {
		t.Errorf("FactHit=false, want true")
	}
}
