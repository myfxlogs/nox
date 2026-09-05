package cnfi

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// 断流告警演练（P2 验收项；C4 市场日历制 + F4 calendar_unknown）。
func TestWatchdogOnce_CalendarGating(t *testing.T) {
	st, err := pgstore.New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_calendar WHERE cal_date >= '2099-01-01'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM alerts WHERE ts >= now() - interval '1 minute' AND message LIKE 'CN-FI 演练%'`)
	})

	c := NewMarketCollector(st)
	// nowFn 钉到 2099-01-03 22:00 CST（营业日、已过 21:30 截止点）。
	now := time.Date(2099, 1, 3, 22, 0, 0, 0, c.location)
	c.nowFn = func() time.Time { return now }

	var alerts []string
	collectAlert := func(level, msg string) error {
		alerts = append(alerts, level+"|"+msg)
		return nil
	}

	// 场景 1：calendar_unknown（无日历行）→ 不产断流结论，零告警。
	c.WatchdogOnce(ctx, collectAlert)
	if len(alerts) != 0 {
		t.Fatalf("calendar_unknown must not alert, got %v", alerts)
	}

	// 场景 2：营业日 + 未采集 → warn 告警。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO cnfi_calendar (cal_date, is_business_day, cal_version) VALUES ('2099-01-03', true, 'test')`); err != nil {
		t.Fatalf("seed calendar: %v", err)
	}
	c.WatchdogOnce(ctx, collectAlert)
	if len(alerts) != 1 || alerts[0][0:4] != "warn" {
		t.Fatalf("business day uncollected must warn, got %v", alerts)
	}

	// 场景 3：周末（假日行）→ 零告警。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO cnfi_calendar (cal_date, is_business_day, cal_version) VALUES ('2099-01-04', false, 'test')`); err != nil {
		t.Fatalf("seed weekend: %v", err)
	}
	c.nowFn = func() time.Time { return time.Date(2099, 1, 4, 22, 0, 0, 0, c.location) }
	c.WatchdogOnce(ctx, collectAlert)
	if len(alerts) != 1 {
		t.Fatalf("weekend must not alert, got %v", alerts)
	}

	// 场景 4：已采集（max as_of >= today）→ 零告警。
	day := "2099-01-03"
	if _, _, err := st.UpsertCNFISeries(ctx, []pgstore.CNFIPoint{
		{AsOf: day, Series: "cgb", Tenor: "10y", ValueBP: 168.04},
	}, "test", "t", time.Now().UTC()); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_series WHERE as_of='2099-01-03'`)
	})
	c.nowFn = func() time.Time { return now }
	c.WatchdogOnce(ctx, collectAlert)
	if len(alerts) != 1 {
		t.Fatalf("collected day must not alert, got %v", alerts)
	}
}
