// D-259 P2 S4 断言①②④（先红后绿）：基金信号评估 state 行 + 共享 helpers.
package cnfi

import (
	"context"
	"fmt"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// d259FundSpec describes one fund to seed in d259Setup.
type d259FundSpec struct {
	code string
	ljjz []float64 // ascending daily LJJZ series (from oldest to today)
	hold bool
}

// d259Setup seeds candidates in `layer` with NAV series; hold=true marks holdings.
func d259Setup(t *testing.T, ctx context.Context, st *pgstore.Store, layer string, funds []d259FundSpec) []string {
	t.Helper()
	today := time.Now().UTC()
	codes := make([]string, 0, len(funds))
	for _, f := range funds {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO cnfi_instruments (code,kind,name,is_active,layer) VALUES ($1,'fund',$2,true,$3)
			 ON CONFLICT (code) DO UPDATE SET layer=EXCLUDED.layer, is_active=true`,
			f.code, f.code+"-name", layer); err != nil {
			t.Fatalf("instrument %s: %v", f.code, err)
		}
		for i, ljjz := range f.ljjz {
			day := today.AddDate(0, 0, -(len(f.ljjz) - 1 - i)).Format("2006-01-02")
			if err := st.UpsertCNFINAV(ctx, f.code, day, 1.0, &ljjz, "test", time.Now().UTC()); err != nil {
				t.Fatalf("nav %s %s: %v", f.code, day, err)
			}
		}
		if f.hold {
			if _, err := st.Pool().Exec(ctx,
				`INSERT INTO cnfi_holdings (code, layer, qty, cost_value, opened_date, note)
				 VALUES ($1, $2, 1, 1, '2026-01-01', 'd259-test')
				 ON CONFLICT DO NOTHING`, f.code, layer); err != nil {
				t.Fatalf("holding %s: %v", f.code, err)
			}
		}
		codes = append(codes, f.code)
	}
	return codes
}

func d259Cleanup(t *testing.T, ctx context.Context, st *pgstore.Store, codes []string) {
	t.Helper()
	for _, c := range codes {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_signal_state WHERE key LIKE $1`, fmt.Sprintf("%%_%s", c))
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_nav WHERE code=$1`, c)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_holdings WHERE code=$1`, c)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_instruments WHERE code=$1`, c)
	}
	_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_signal_state WHERE key LIKE 'layer_dd_%'`)
	_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_signal_config WHERE key IN ('underperf_bps','layer_dd_pct','rank_shift','alerts_enabled')`)
	_, _ = st.Pool().Exec(ctx, `DELETE FROM alerts WHERE message LIKE 'CN-FI%'`)
}

// 断言①：基金跑输层内中位 ≥20bp → fund_underperf_<code> state + passes_screen=false.
func TestD259_FundUnderperfSignalAndScreenFail(t *testing.T) {
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
	// 持仓基金 A：1m 收益 0%（ljjz 1.00→1.00）；候选 B：1m 收益 0.5%（ljjz 1.00→1.005）
	// 中位=0.25%，A 跑输 25bp ≥20bp → 命中①
	funds := []d259FundSpec{
		{"D259A", []float64{1.00, 1.00}, true},   // 持仓，0% 收益
		{"D259B", []float64{1.00, 1.005}, false}, // 候选，0.5% 收益
	}
	codes := d259Setup(t, ctx, st, "coupon", funds)
	t.Cleanup(func() { d259Cleanup(t, ctx, st, codes) })

	if err := EvaluateSignals(ctx, st, true); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	key := "fund_underperf_D259A"
	var state string
	var asOf string
	err = st.Pool().QueryRow(ctx,
		`SELECT observed_state, observed_as_of::text FROM cnfi_signal_state WHERE key=$1`, key).Scan(&state, &asOf)
	if err != nil {
		t.Fatalf("① state missing: %v", err)
	}
	if state != "observe" {
		t.Fatalf("① state=%q want observe", state)
	}
	// passes_screen via CNFIActiveSignalKeys
	active, err := st.CNFIActiveSignalKeys(ctx, 7)
	if err != nil {
		t.Fatalf("active keys: %v", err)
	}
	if !active[key] {
		t.Fatalf("① active keys missing %s", key)
	}
}

// 断言②：层组合 3m 回撤 ≥0.5% → layer_dd_<layer> state.
func TestD259_LayerDDSignal(t *testing.T) {
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
	// 持仓基金：3m 序列 1.00→1.10→1.00（回撤 ~9% ≥0.5%）
	funds := []d259FundSpec{
		{"D259DD", []float64{1.00, 1.10, 1.00}, true},
	}
	codes := d259Setup(t, ctx, st, "coupon", funds)
	t.Cleanup(func() { d259Cleanup(t, ctx, st, codes) })

	if err := EvaluateSignals(ctx, st, true); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	key := "layer_dd_coupon"
	var state string
	err = st.Pool().QueryRow(ctx,
		`SELECT observed_state FROM cnfi_signal_state WHERE key=$1`, key).Scan(&state)
	if err != nil {
		t.Fatalf("② state missing: %v", err)
	}
	if state != "observe" {
		t.Fatalf("② state=%q want observe", state)
	}
}

// 断言④：持仓基金排名单日变动 ≥2 位 → rank_shift_<code> state.
func TestD259_RankShiftSignal(t *testing.T) {
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
	// 3 基金：A 持仓（1y 收益最低=rank 3）；先写入 rank_prev_A=1（前一天 rank=1）
	// 当次评估 rank=3，变动 2 位 ≥2 → 命中④
	funds := []d259FundSpec{
		{"D259RA", []float64{1.00, 1.00, 1.00}, true},   // 持仓，0% 收益=rank 3
		{"D259RB", []float64{1.00, 1.01, 1.02}, false},  // 候选，2% 收益=rank 1
		{"D259RC", []float64{1.00, 1.005, 1.01}, false}, // 候选，1% 收益=rank 2
	}
	codes := d259Setup(t, ctx, st, "coupon", funds)
	t.Cleanup(func() { d259Cleanup(t, ctx, st, codes) })

	// 预置 rank_prev_D259RA=1（模拟前一天 rank=1）
	if err := st.UpsertCNFISignalState(ctx, "rank_prev_D259RA", "observe", 1.0, 3,
		time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"), "test-prev"); err != nil {
		t.Fatalf("seed rank_prev: %v", err)
	}

	if err := EvaluateSignals(ctx, st, true); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	key := "rank_shift_D259RA"
	var state string
	err = st.Pool().QueryRow(ctx,
		`SELECT observed_state FROM cnfi_signal_state WHERE key=$1`, key).Scan(&state)
	if err != nil {
		t.Fatalf("④ state missing: %v", err)
	}
	if state != "observe" {
		t.Fatalf("④ state=%q want observe", state)
	}
}
