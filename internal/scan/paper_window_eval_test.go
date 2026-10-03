// paper_window_eval_test.go —— D-033① 锚：cohort 健康评估（fail-closed/停滞/分桶）。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

type fakeEvalStore struct {
	facts     map[string][]fact.Fact // kind|venue|symbol → latest
	factsHist map[string][]fact.Fact
	mappings  map[string][]pgstore.SymbolMapping
	uni       map[string]pgstore.CexUniverseRow
}

func evalKey(kind, venue, symbol string) string { return kind + "|" + venue + "|" + symbol }

func (f *fakeEvalStore) LatestFacts(_ context.Context, kind, venue, symbol string) ([]fact.Fact, error) {
	return f.facts[evalKey(kind, venue, symbol)], nil
}
func (f *fakeEvalStore) FactsSince(_ context.Context, kind, venue, symbol string, _ time.Time) ([]fact.Fact, error) {
	return f.factsHist[evalKey(kind, venue, symbol)], nil
}
func (f *fakeEvalStore) ListSymbolMappings(_ context.Context, venue string) ([]pgstore.SymbolMapping, error) {
	return f.mappings[venue], nil
}
func (f *fakeEvalStore) TicksWindow(context.Context, string, string, time.Time) ([]pgstore.TickRow, error) {
	return nil, nil
}
func (f *fakeEvalStore) ListCexUniverse(context.Context) (map[string]pgstore.CexUniverseRow, error) {
	return f.uni, nil
}

var evalNow = time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
var evalSince = time.Date(2026, 10, 3, 1, 59, 0, 0, time.UTC)

func evalFixtures() *fakeEvalStore {
	now := evalNow
	return &fakeEvalStore{
		uni: map[string]pgstore.CexUniverseRow{
			"okx|XAU-USDT-SWAP": {Venue: "okx", Symbol: "XAU-USDT-SWAP", HedgeCanonical: "XAUUSD"},
		},
		mappings: map[string][]pgstore.SymbolMapping{
			"3001157": {{Venue: "3001157", BrokerSymbol: "XAUUSDm", Canonical: "XAUUSD", IsPrimary: true}},
		},
		facts: map[string][]fact.Fact{
			evalKey(fact.KindFunding, "okx", "XAUUSDT"):           {{Value: 20, Ts: now}},
			evalKey(fact.KindFundingNextSettle, "okx", "XAUUSDT"): {{Value: float64(now.Add(7 * time.Hour).Unix()), Ts: now}},
			evalKey(fact.KindFundingInterval, "okx", "XAUUSDT"):   {{Value: 28800, Ts: now}},
			evalKey(fact.KindSwapLong, "3001157", "XAUUSDm"):      {{Value: -30, Ts: now}},
		},
		factsHist: map[string][]fact.Fact{},
	}
}

func mkPaperRow(id int64, status string, lastAccrue *time.Time) pgstore.PaperSpotCarry {
	return pgstore.PaperSpotCarry{
		ID: id, VenueA: "3001157", VenueB: "okx", Symbol: "XAUUSD",
		Side: "short_perp", Structure: "long_cfd_short_perp",
		EntryTs: evalSince.Add(time.Hour), QtyUSD: 1000,
		AccruedUSD: 1.5, LastAccrueTs: lastAccrue, Status: status,
	}
}

func TestD033_EvalHealthyRow(t *testing.T) {
	recent := evalNow.Add(-time.Hour) // 水位 1h 前 → 远小于 48h 停滞阈
	rows := []pgstore.PaperSpotCarry{mkPaperRow(1, "active", &recent)}
	ev, err := EvalPaperCohort(context.Background(), evalFixtures(), rows, evalSince, evalNow)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ActiveN != 1 || ev.FailClosed != 0 || ev.Stalled != 0 {
		t.Fatalf("eval: %+v", ev)
	}
	if ev.WindowNet != 1.5 || ev.NotionalUSD != 1000 {
		t.Fatalf("sums: %+v", ev)
	}
}

func TestD033_EvalFailClosed(t *testing.T) {
	st := evalFixtures()
	delete(st.facts, evalKey(fact.KindFundingNextSettle, "okx", "XAUUSDT")) // 边界事实缺
	recent := evalNow.Add(-time.Hour)
	rows := []pgstore.PaperSpotCarry{mkPaperRow(1, "active", &recent)}
	ev, err := EvalPaperCohort(context.Background(), st, rows, evalSince, evalNow)
	if err != nil {
		t.Fatal(err)
	}
	if ev.FailClosed != 1 || ev.Stalled != 0 {
		t.Fatalf("eval: %+v", ev)
	}
}

func TestD033_EvalStalled(t *testing.T) {
	old := evalNow.Add(-72 * time.Hour) // 落后 >2×24h(MT 日界)
	// bounds 落在 (base,now] 内需 rateAt——CurOK=true 回落当期率，aok 仍 true
	rows := []pgstore.PaperSpotCarry{mkPaperRow(1, "active", &old)}
	ev, err := EvalPaperCohort(context.Background(), evalFixtures(), rows, evalSince, evalNow)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Stalled != 1 || ev.FailClosed != 0 {
		t.Fatalf("eval: %+v", ev)
	}
}

func TestD033_EvalExitBuckets(t *testing.T) {
	exitTs := evalNow.Add(-24 * time.Hour)
	ex := mkPaperRow(2, "exited", nil)
	ex.ExitTs = &exitTs
	ex.ExitReason = "normalized"
	ex.AccruedUSD = 0.75
	stale := mkPaperRow(3, "exited", nil)
	old := evalSince.Add(-time.Hour) // 窗前退出 → 不计
	stale.ExitTs = &old
	recent := evalNow.Add(-time.Hour)
	rows := []pgstore.PaperSpotCarry{mkPaperRow(1, "active", &recent), ex, stale}
	ev, err := EvalPaperCohort(context.Background(), evalFixtures(), rows, evalSince, evalNow)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ExitedN != 1 || ev.NetExited != 0.75 {
		t.Fatalf("exits: %+v", ev)
	}
	if len(ev.ExitedByDay) != 1 || ev.ExitedByDay[0].Rows != 1 || ev.ExitedByDay[0].Reasons[0] != "normalized" {
		t.Fatalf("buckets: %+v", ev.ExitedByDay)
	}
	if ev.WindowNet != 1.5+0.75 {
		t.Fatalf("window net: %v", ev.WindowNet)
	}
}
