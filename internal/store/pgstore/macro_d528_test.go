// macro_d528_test.go —— D-528 S9 A1：macro_event upsert 幂等锚（D-120 清退 cot/fred 段）（同批两写行数不变）。
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

}
