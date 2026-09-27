// macro_d528_test.go —— D-528 S9 A1：三表 upsert 幂等锚（同批两写行数不变）。
// 需 ARB_TEST_PG_DSN（_test 库，逐包隔离）。
package pgstore

import (
	"context"
	"testing"
	"time"

	"arb/internal/testutil"
)

func TestD528_MacroUpsertIdempotent(t *testing.T) {
	s, err := New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	et := time.Date(2026, 10, 2, 12, 30, 0, 0, time.UTC)
	events := []MacroEvent{{
		Source: "mql5-test", EventTime: et, Currency: "USD", Name: "Nonfarm Payrolls",
		Importance: "high", Forecast: "170K", Previous: "168K", Actual: "", Raw: []byte(`{"Id":1}`),
	}}
	// 同批含新值（actual 补发布）写两次——行数不变、值更新。
	events[0].Actual = "175K"
	for round := 1; round <= 2; round++ {
		n, err := s.UpsertMacroEvents(ctx, events)
		if err != nil || n != 1 {
			t.Fatalf("round%d: n=%d err=%v", round, n, err)
		}
	}
	got, err := s.MacroEventsBetween(ctx, et.Add(-time.Hour), et.Add(time.Hour))
	if err != nil || len(got) != 1 {
		t.Fatalf("between: n=%d err=%v", len(got), err)
	}
	if got[0].Actual != "175K" {
		t.Fatalf("conflict 更新失败: actual=%q", got[0].Actual)
	}

	rdate := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	cot := []CotPosition{{
		ReportDate: rdate, Instrument: "6JTEST", Category: "lev_fund",
		Direction: "LONG", NetPosition: 4000, NetChange: 3000, Raw: []byte(`{}`),
	}}
	for round := 1; round <= 2; round++ {
		n, err := s.UpsertCotPositions(ctx, cot)
		if err != nil || n != 1 {
			t.Fatalf("cot round%d: n=%d err=%v", round, n, err)
		}
	}
	rows, err := s.LatestCot(ctx, "6JTEST", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("latest: n=%d err=%v", len(rows), err)
	}
	if rows[0].NetPosition != 4000 {
		t.Fatalf("cot 回读错: %+v", rows[0])
	}
	base, err := s.MaxCotReportDate(ctx)
	if err != nil || base.Before(rdate) {
		t.Fatalf("max report_date: %v err=%v", base, err)
	}

	obs := []FredObs{{SeriesID: "DGS2TEST", ObsDate: rdate, Value: 4.12}}
	for round := 1; round <= 2; round++ {
		n, err := s.UpsertFredObs(ctx, obs)
		if err != nil || n != 1 {
			t.Fatalf("fred round%d: n=%d err=%v", round, n, err)
		}
	}
}
