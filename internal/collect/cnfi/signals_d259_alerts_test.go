// D-259 P2 S4 断言⑤⑤b⑤c（先红后绿）：alerts 总开关 + 同日幂等.
package cnfi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// 断言⑤：alerts_enabled=false → alerts 表无新增 CN-FI 行.
func TestD259_NoAlertsWhenDisabled(t *testing.T) {
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
	// 触发①（跑输 ≥20bp）
	funds := []d259FundSpec{
		{"D259NA", []float64{1.00, 1.00}, true},
		{"D259NB", []float64{1.00, 1.005}, false},
	}
	codes := d259Setup(t, ctx, st, "coupon", funds)
	t.Cleanup(func() { d259Cleanup(t, ctx, st, codes) })

	// 确保 alerts_enabled=false（默认）
	cfg, _ := st.LoadCNFIFundSignalConfig(ctx)
	if cfg.AlertsEnabled {
		t.Fatal("alerts_enabled default should be false")
	}

	var before int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE message LIKE 'CN-FI%'`).Scan(&before)

	if err := EvaluateSignals(ctx, st, true); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	var after int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE message LIKE 'CN-FI%'`).Scan(&after); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if after != before {
		t.Fatalf("⑤ alerts_enabled=false but alerts increased: before=%d after=%d", before, after)
	}
}

// 断言⑤b：同日重跑幂等（observed_as_of == today → 跳过，state 不重复写）.
func TestD259_SameDayIdempotent(t *testing.T) {
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
	funds := []d259FundSpec{
		{"D259IA", []float64{1.00, 1.00}, true},
		{"D259IB", []float64{1.00, 1.005}, false},
	}
	codes := d259Setup(t, ctx, st, "coupon", funds)
	t.Cleanup(func() { d259Cleanup(t, ctx, st, codes) })

	// 第一次评估
	if err := EvaluateSignals(ctx, st, true); err != nil {
		t.Fatalf("first evaluate: %v", err)
	}
	var updatedAt1 time.Time
	if err := st.Pool().QueryRow(ctx,
		`SELECT updated_at FROM cnfi_signal_state WHERE key='fund_underperf_D259IA'`).Scan(&updatedAt1); err != nil {
		t.Fatalf("first state missing: %v", err)
	}
	// 第二次评估（同日）
	time.Sleep(10 * time.Millisecond)
	if err := EvaluateSignals(ctx, st, true); err != nil {
		t.Fatalf("second evaluate: %v", err)
	}
	var updatedAt2 time.Time
	if err := st.Pool().QueryRow(ctx,
		`SELECT updated_at FROM cnfi_signal_state WHERE key='fund_underperf_D259IA'`).Scan(&updatedAt2); err != nil {
		t.Fatalf("second state missing: %v", err)
	}
	// 幂等：updated_at 不变（observed_as_of == today → 跳过 upsert）
	if !updatedAt2.Equal(updatedAt1) {
		t.Fatalf("⑤b idempotency violated: updated_at changed (first=%v second=%v)", updatedAt1, updatedAt2)
	}
}

// 断言⑤c：alerts_enabled=true → 触发①时 alerts 表新增 CN-FI 行（验证总开关接线）.
func TestD259_AlertsWhenEnabled(t *testing.T) {
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
	// 启用 alerts
	alertsTrue, _ := json.Marshal(true)
	if err := st.UpsertCNFISignalConfigRaw(ctx, "alerts_enabled", alertsTrue, "D-259-test", "test"); err != nil {
		t.Fatalf("enable alerts: %v", err)
	}
	funds := []d259FundSpec{
		{"D259EA", []float64{1.00, 1.00}, true},
		{"D259EB", []float64{1.00, 1.005}, false},
	}
	codes := d259Setup(t, ctx, st, "coupon", funds)
	t.Cleanup(func() { d259Cleanup(t, ctx, st, codes) })

	var before int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE message LIKE 'CN-FI%'`).Scan(&before)

	if err := EvaluateSignals(ctx, st, true); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	var after int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE message LIKE 'CN-FI%'`).Scan(&after); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if after <= before {
		t.Fatalf("alerts_enabled=true but no alert inserted: before=%d after=%d", before, after)
	}
}
