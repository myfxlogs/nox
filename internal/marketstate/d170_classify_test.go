// D-170：决策树七 verdict 全分支表测（硬锚）+ VenueAllClosed venue 级判。
package marketstate

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// fakeMS 裁决面替身（全字段可控；零值=证据缺态）。
type fakeMS struct {
	sessions   []fact.Fact // LatestFactsByKind(trade_session)
	tmFacts    []fact.Fact // FactsSince(trade_mode)
	tickLatest time.Time   // VenueTickLatestAt
	tickOK     bool
	venues     []string       // VenuesByCanonical
	moveN      map[string]int // venue→参照窗 DISTINCT bid
	holiday    bool           // macro holiday 佐证
}

func (f *fakeMS) LatestFactsByKind(context.Context, string) ([]fact.Fact, error) {
	return f.sessions, nil
}
func (f *fakeMS) FactsSince(context.Context, string, string, string, time.Time) ([]fact.Fact, error) {
	return f.tmFacts, nil
}
func (f *fakeMS) TicksPriceChanged(_ context.Context, venue, _ string, _ time.Duration) (bool, int, error) {
	n := f.moveN[venue]
	return n >= 2, n, nil
}
func (f *fakeMS) VenueTickLatestAt(context.Context, string) (time.Time, bool, error) {
	return f.tickLatest, f.tickOK, nil
}
func (f *fakeMS) VenuesByCanonical(context.Context, string) ([]string, error) {
	return f.venues, nil
}
func (f *fakeMS) MacroEventsBetween(context.Context, time.Time, time.Time) ([]pgstore.MacroEvent, error) {
	if f.holiday {
		return []pgstore.MacroEvent{{Importance: "holiday"}}, nil
	}
	return nil, nil
}

// sessFact 编码周表窗：v = wday*1e8 + startMin*1e4 + endMin（同 adapter 落库编码）。
func sessFact(venue, sym string, wday, start, end int) fact.Fact {
	return fact.Fact{Venue: venue, Symbol: sym, Value: float64(wday*1e8 + start*1e4 + end)}
}

// tmFact trade_mode 行（ts 升序排——FactsSince 返回序）。
func tmFact(v float64, ts time.Time) fact.Fact { return fact.Fact{Value: v, Ts: ts} }

// testNow 固定周一 12:00 UTC（2026-10-12 Monday）。
var testNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// openAllDay 当日全天窗（wday=周一 start=end=0）。
func openAllDay(venue, sym string) fact.Fact { return sessFact(venue, sym, 1, 0, 0) }

func TestD170_ClassifyBranches(t *testing.T) {
	live := testNow.Add(-time.Minute)
	cases := []struct {
		name string
		f    *fakeMS
		want Verdict
		lvl  string
	}{
		{"closed_闭市窗", &fakeMS{
			sessions: []fact.Fact{sessFact("v1", "XAU", 0, 0, 0)}, // 仅周日全天
			tickOK:   true, tickLatest: live,
		}, Closed, ""},
		{"venue_dead_开市全静", &fakeMS{
			sessions: []fact.Fact{openAllDay("v1", "XAU")},
			tickOK:   true, tickLatest: testNow.Add(-30 * time.Minute), // 帧超龄
		}, VenueDead, "warn"},
		{"broker_disabled_近窗翻", &fakeMS{
			sessions: []fact.Fact{openAllDay("v1", "XAU")},
			tickOK:   true, tickLatest: live,
			tmFacts: []fact.Fact{
				tmFact(fact.TradeModeFull, testNow.Add(-3*time.Hour)),
				tmFact(fact.TradeModeCloseOnly, testNow.Add(-time.Hour)),
			},
		}, BrokerDisabled, "warn"},
		{"leg_dead_broker_他venue在走", &fakeMS{
			sessions: []fact.Fact{openAllDay("v1", "XAU")},
			tickOK:   true, tickLatest: live,
			venues: []string{"v1", "v2"}, moveN: map[string]int{"v2": 5},
		}, LegDeadBroker, "warn"},
		{"leg_frozen_suspect_参照高速动", &fakeMS{
			sessions: []fact.Fact{openAllDay("v1", "XAU")},
			tickOK:   true, tickLatest: live,
			venues: []string{"v1", "v2"}, moveN: map[string]int{"v2": 25},
		}, LegFrozenSuspect, "critical"},
		{"market_wide_quiet_全静假日", &fakeMS{
			sessions: []fact.Fact{openAllDay("v1", "XAU")},
			tickOK:   true, tickLatest: live,
			venues: []string{"v1", "v2"}, holiday: true,
		}, MarketWideQuiet, "info"},
		{"market_wide_quiet_全静无假日", &fakeMS{
			sessions: []fact.Fact{openAllDay("v1", "XAU")},
			tickOK:   true, tickLatest: live,
			venues: []string{"v1", "v2"}, holiday: false,
		}, MarketWideQuiet, "warn"},
		{"独苗独死_归leg_dead_broker", &fakeMS{
			sessions: []fact.Fact{openAllDay("v1", "XAU")},
			tickOK:   true, tickLatest: live,
			venues: []string{"v1"},
		}, LegDeadBroker, "warn"},
		{"unknown_全静表缺席", &fakeMS{
			sessions: nil, tickOK: false, // session 表缺席+ticks 零行
		}, Unknown, "warn"},
		{"unknown_venue活无证据", &fakeMS{
			sessions: nil, tickOK: true, tickLatest: live,
		}, Unknown, "warn"},
	}
	for _, tc := range cases {
		got := ClassifyLegSilence(context.Background(), tc.f, "v1", "XAU", testNow)
		if got.Verdict != tc.want || got.Level != tc.lvl {
			t.Errorf("%s: got %s/%s, want %s/%s", tc.name, got.Verdict, got.Level, tc.want, tc.lvl)
		}
	}
}

func TestD170_ClassifyCEXSkip(t *testing.T) {
	f := &fakeMS{}
	got := ClassifyLegSilence(context.Background(), f, "okx:perp", "XAU", testNow)
	if got.Verdict != NotApplicable {
		t.Fatalf("cex got %s, want not_applicable", got.Verdict)
	}
}

func TestD170_VenueAllClosed(t *testing.T) {
	// 全周日窗 → closed。
	f := &fakeMS{sessions: []fact.Fact{sessFact("v1", "XAU", 0, 0, 0), sessFact("v1", "EUR", 0, 0, 0)}}
	closed, known := VenueAllClosed(context.Background(), f, "v1", testNow)
	if !closed || !known {
		t.Fatalf("want closed+known, got %v/%v", closed, known)
	}
	// 任一符号开市 → 非静默。
	f2 := &fakeMS{sessions: []fact.Fact{sessFact("v1", "XAU", 0, 0, 0), openAllDay("v1", "EUR")}}
	closed, known = VenueAllClosed(context.Background(), f2, "v1", testNow)
	if closed || !known {
		t.Fatalf("open venue: got %v/%v, want false/true", closed, known)
	}
	// 表缺席 → known=false（调用方回退兜底）。
	closed, known = VenueAllClosed(context.Background(), &fakeMS{}, "v1", testNow)
	if closed || known {
		t.Fatalf("empty table: got %v/%v, want false/false", closed, known)
	}
}

func TestD170_TradeModeFlip(t *testing.T) {
	// 翻转（活→死）=true。
	if !tradeModeRecentlyDisabled([]fact.Fact{
		tmFact(fact.TradeModeFull, testNow.Add(-2*time.Hour)),
		tmFact(fact.TradeModeDisabled, testNow.Add(-time.Hour)),
	}) {
		t.Error("Full→Disabled 应判翻转")
	}
	// 一直死值（无活值先例）=false（无翻转证据）。
	if tradeModeRecentlyDisabled([]fact.Fact{
		tmFact(fact.TradeModeDisabled, testNow.Add(-3*time.Hour)),
		tmFact(fact.TradeModeDisabled, testNow.Add(-time.Hour)),
	}) {
		t.Error("全死值不应判翻转")
	}
	// 一直活=false。
	if tradeModeRecentlyDisabled([]fact.Fact{tmFact(fact.TradeModeFull, testNow)}) {
		t.Error("活值不应判翻转")
	}
}
