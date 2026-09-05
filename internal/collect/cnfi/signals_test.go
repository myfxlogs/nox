package cnfi

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// 历史回放（D-229 R-F5/D-230 F5）：合成 300 个业务日 ascending 派生序列，
// 断言 pct-v1 数学（mid-rank/窗口/当前点不入窗）与三档行为表可复现。
func TestEvaluateSignals_ReplayDeterministic(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_series WHERE as_of >= '2100-01-01'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_signal_state WHERE key = $1`, signalKey3yPct)
	})

	// 合成：ig_aaa(3y)=100+i*0.1bp，cgb(3y)=50 恒定 → 派生=50+i*0.1，i=0..299 严格递增。
	// as_of 用 2100-01-01 起的合成业务日序列（连续日，pct-v1 按 distinct as_of 计数）。
	base := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	const days = 300
	for i := 0; i < days; i++ {
		day := base.AddDate(0, 0, i).Format("2006-01-02")
		for _, row := range []pgstore.CNFIPoint{
			{AsOf: day, Series: "ig_aaa", Tenor: "3y", ValueBP: 100 + float64(i)*0.1},
			{AsOf: day, Series: "cgb", Tenor: "3y", ValueBP: 50},
		} {
			if _, _, err := st.UpsertCNFISeries(ctx, []pgstore.CNFIPoint{row}, "test", "replay", time.Now().UTC()); err != nil {
				t.Fatalf("seed i=%d: %v", i, err)
			}
		}
	}

	// 当前点=i=299（派生=79.9）；窗口=严格更早 250 日（i=49..298 → 派生 54.9..79.8）。
	// mid-rank：窗口全部 < 79.9 → pct = 250/250×100 = 100.0；sample_n=250 → tier ok。
	if err := EvaluateSignals(ctx, st, true); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	var state string
	var lastValue float64
	var sampleN int
	var observedAsOf string
	err = st.Pool().QueryRow(ctx,
		`SELECT observed_state, last_value::float8, sample_n, observed_as_of::text FROM cnfi_signal_state WHERE key=$1`,
		signalKey3yPct).Scan(&state, &lastValue, &sampleN, &observedAsOf)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if lastValue != 100.0 {
		t.Fatalf("pctile = %v, want 100.0 (全窗口小于当前点)", lastValue)
	}
	if sampleN != 250 {
		t.Fatalf("sample_n = %d, want 250 (当前点不入窗)", sampleN)
	}
	if QualityTier(sampleN) != "ok" {
		t.Fatalf("tier = %s, want ok", QualityTier(sampleN))
	}
	if state != "observe" {
		t.Fatalf("observe-only 期 state = %q, want observe（C11）", state)
	}
	if observedAsOf != base.AddDate(0, 0, days-1).Format("2006-01-02") {
		t.Fatalf("observed_as_of = %q", observedAsOf)
	}

	// 数学单元：中位值（窗口含并列）→ 50.0；空窗口 → 0
	if got := PercentilePctV1([]float64{10, 20, 20, 30}, 20); got != 50.0 {
		t.Fatalf("mid-rank 并列 = %v, want 50.0", got)
	}
	if got := PercentilePctV1(nil, 5); got != 0 {
		t.Fatalf("empty window = %v, want 0", got)
	}
	// 三档行为表
	for _, tc := range []struct {
		n    int
		want string
	}{
		{250, "ok"}, {249, "reduced"}, {120, "reduced"}, {119, "N"},
	} {
		if got := QualityTier(tc.n); got != tc.want {
			t.Fatalf("QualityTier(%d) = %s, want %s", tc.n, got, tc.want)
		}
	}
}
