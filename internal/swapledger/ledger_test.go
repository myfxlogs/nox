package swapledger

import (
	"math"
	"testing"
	"time"

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
	longMap := map[factKey]float64{factKey{"AUDJPY", "vA"}: 5.0} // 5 bps/日
	shortMap := map[factKey]float64{}
	fundMap := map[factKey]float64{}
	notional := stubNotional{vals: map[string]float64{"AUDJPY:vA:mt5:ask": 100000}} // 1 手=10万 USD
	r := stubTripleDay{venueDays: map[string]time.Weekday{"vA": time.Wednesday}}
	l := &Ledger{notional: notional, tripleDay: r}

	leg := pgstore.PositionLeg{Venue: "vA:mt5", Side: "BUY", Qty: 2.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC) // Monday → multiplier=1
	got, ok := l.legSwapAmount("AUDJPY", leg, monday, false, longMap, shortMap, fundMap)
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
	longMap := map[factKey]float64{}
	shortMap := map[factKey]float64{factKey{"AUDJPY", "vB"}: -3.0} // -3 bps/日（付 swap）
	fundMap := map[factKey]float64{}
	notional := stubNotional{vals: map[string]float64{"AUDJPY:vB:mt5:bid": 100000}}
	r := stubTripleDay{venueDays: map[string]time.Weekday{"vB": time.Wednesday}}
	l := &Ledger{notional: notional, tripleDay: r}

	leg := pgstore.PositionLeg{Venue: "vB:mt5", Side: "SELL", Qty: 1.0}
	wed := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC) // Wednesday → multiplier=3
	got, ok := l.legSwapAmount("AUDJPY", leg, wed, false, longMap, shortMap, fundMap)
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
func TestLegSwapAmount_FundingType(t *testing.T) {
	longMap := map[factKey]float64{}
	shortMap := map[factKey]float64{}
	fundMap := map[factKey]float64{factKey{"BTCUSDT", "binance"}: 10.95} // 10.95% 年化
	notional := stubNotional{vals: map[string]float64{"BTCUSDT:binance:spot:ask": 50000}}
	r := stubTripleDay{}
	l := &Ledger{notional: notional, tripleDay: r}

	leg := pgstore.PositionLeg{Venue: "binance:spot", Side: "BUY", Qty: 1.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	got, ok := l.legSwapAmount("BTCUSDT", leg, monday, true, longMap, shortMap, fundMap)
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

// TestLegSwapAmount_RateNotFound swap rate 缺失 → fail-closed ok=false。
func TestLegSwapAmount_RateNotFound(t *testing.T) {
	notional := stubNotional{vals: map[string]float64{"AUDJPY:vA:mt5:ask": 100000}}
	r := stubTripleDay{}
	l := &Ledger{notional: notional, tripleDay: r}

	leg := pgstore.PositionLeg{Venue: "vA:mt5", Side: "BUY", Qty: 1.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	_, ok := l.legSwapAmount("AUDJPY", leg, monday, false, map[factKey]float64{}, map[factKey]float64{}, map[factKey]float64{})
	if ok {
		t.Error("rate not found want ok=false, got true")
	}
}

// TestLegSwapAmount_NotionalNotFound notional 缺失 → fail-closed ok=false。
func TestLegSwapAmount_NotionalNotFound(t *testing.T) {
	longMap := map[factKey]float64{factKey{"AUDJPY", "vA"}: 5.0}
	notional := stubNotional{vals: map[string]float64{}} // 空 = notional 不可用
	r := stubTripleDay{}
	l := &Ledger{notional: notional, tripleDay: r}

	leg := pgstore.PositionLeg{Venue: "vA:mt5", Side: "BUY", Qty: 1.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	_, ok := l.legSwapAmount("AUDJPY", leg, monday, false, longMap, map[factKey]float64{}, map[factKey]float64{})
	if ok {
		t.Error("notional not found want ok=false, got true")
	}
}

// TestLegSwapAmount_ZeroRate 零息 venue（rate=0）→ amount=0（不写 cashflow）。
func TestLegSwapAmount_ZeroRate(t *testing.T) {
	longMap := map[factKey]float64{factKey{"AUDJPY", "vA"}: 0.0} // 零息
	notional := stubNotional{vals: map[string]float64{"AUDJPY:vA:mt5:ask": 100000}}
	r := stubTripleDay{venueDays: map[string]time.Weekday{"vA": time.Wednesday}}
	l := &Ledger{notional: notional, tripleDay: r}

	leg := pgstore.PositionLeg{Venue: "vA:mt5", Side: "BUY", Qty: 1.0}
	monday := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	got, ok := l.legSwapAmount("AUDJPY", leg, monday, false, longMap, map[factKey]float64{}, map[factKey]float64{})
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

// TestCashflowKind 按类型返回 kind。
func TestCashflowKind(t *testing.T) {
	if got := cashflowKind("SWAPARB"); got != "swap" {
		t.Errorf("SWAPARB kind want swap, got %s", got)
	}
	if got := cashflowKind("CARRY"); got != "swap" {
		t.Errorf("CARRY kind want swap, got %s", got)
	}
	if got := cashflowKind("FUNDING_HEDGE"); got != "funding" {
		t.Errorf("FUNDING_HEDGE kind want funding, got %s", got)
	}
	if got := cashflowKind("PERP_FUNDING"); got != "funding" {
		t.Errorf("PERP_FUNDING kind want funding, got %s", got)
	}
}

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
