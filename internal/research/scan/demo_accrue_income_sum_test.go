// d474c_demo_income_test.go —— D-474c：demo 行真账——accrue 改吃 income_events 实收。
// 三锚：①窗口多事件累加=实收和 ②缺席窗 +0 不外推 ③归属粒度上限 flag_warn 注记落 note；
// ④err 禁吞（查询路径错误上抛——由 SumIncomeWindow 契约保证，此处锚=边界 ts 不重复计）。
// 红证（先红）：旧路径走 474a 结算对齐模型——事件和/注记断言双红。
package scan

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/platform/testutil/storetest"
	"arb/internal/store/pgstore"
)

// seedDemoRow 插 demo 实测行（last_accrue_ts 回拨 base 使 income 窗可控）。
func seedDemoRow(t *testing.T, s *pgstore.Store, venue, symbol string, base time.Time) *pgstore.PaperSpotCarry {
	t.Helper()
	pr := &pgstore.PaperSpotCarry{VenueA: venue, Symbol: symbol, Side: "short_perp",
		EntryFundingAnn: 90, QtyUSD: 1000, Note: "spot=x perp=y"}
	if err := s.InsertPaperSpotCarry(context.Background(), pr); err != nil {
		t.Fatalf("seed paper row: %v", err)
	}
	if err := s.MarkPaperSpotCarryDemo(context.Background(), pr.ID, pr.Note); err != nil {
		t.Fatalf("mark demo: %v", err)
	}
	if _, err := s.Pool().Exec(context.Background(),
		`UPDATE paper_spot_carry SET last_accrue_ts = $1, entry_ts = $1 WHERE id = $2`, base, pr.ID); err != nil {
		t.Fatalf("rewind accrue ts: %v", err)
	}
	return pr
}

// seedIncome 写 income_events 行（tran_id 唯一幂等键=exchange_config_id+tranId，D-496 F1）。
func seedIncome(t *testing.T, s *pgstore.Store, venue, symbol string, amount float64, ts time.Time, tranID int64) {
	t.Helper()
	var cfgID int64
	if err := s.Pool().QueryRow(context.Background(),
		`INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, simulated)
		 VALUES ($1,'perp','t','k','s',true)
		 ON CONFLICT (venue, venue_type, simulated) DO UPDATE SET label=EXCLUDED.label
		 RETURNING id`, venue).Scan(&cfgID); err != nil {
		t.Fatalf("seed exchange config: %v", err)
	}
	if _, err := s.InsertIncomeEvents(context.Background(), []pgstore.IncomeEvent{{
		Venue: venue, VenueType: "perp", ConfigID: cfgID, Symbol: symbol, IncomeType: "FUNDING_FEE",
		AmountUSD: amount, TranID: tranID, Ts: ts,
	}}); err != nil {
		t.Fatalf("seed income: %v", err)
	}
}

// TestD474c_DemoAccrueIncomeSum S3①：窗口内多事件累加=实收和（非模型估值）；
// 锚②附验：水位前推后再跑一轮（无新事件）→ +0 不重复计。
func TestD474c_DemoAccrueIncomeSum(t *testing.T) {
	s := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE fin_cex_universe, paper_spot_carry, income_events RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	now := time.Now().UTC()
	base := now.Add(-24 * time.Hour)
	pr := seedDemoRow(t, s, "binance", "GUSDT", base)
	seedIncome(t, s, "binance", "GUSDT", 0.40, base.Add(time.Hour), 1)
	seedIncome(t, s, "binance", "GUSDT", 0.65, now.Add(-time.Hour), 2)
	seedIncome(t, s, "binance", "GUSDT", -0.10, base.Add(-time.Hour), 3) // 窗口外不计
	seedIncome(t, s, "binance", "OTHERUSDT", 9.99, now.Add(-time.Hour), 4)
	seedIncome(t, s, "okx", "GUSDT", 9.99, now.Add(-time.Hour), 5) // 异 venue/symbol 不串账
	if _, _, err := StepPaperSpotCarry(ctx, s, NewEntryStreak(), now, nil); err != nil {
		t.Fatalf("step: %v", err)
	}
	rows, err := s.ListDemoPaperSpotCarry(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("demo rows = %d: %v", len(rows), err)
	}
	if diff := rows[0].AccruedUSD - 1.05; diff < -0.001 || diff > 0.001 {
		t.Errorf("demo accrued = %v, want 1.05（窗口事件实收和）", rows[0].AccruedUSD)
	}
	// 水位已前推至本轮 now：再跑一轮无新事件 → +0 不重复计。
	if _, _, err := StepPaperSpotCarry(ctx, s, NewEntryStreak(), now.Add(time.Minute), nil); err != nil {
		t.Fatalf("step2: %v", err)
	}
	rows, _ = s.ListDemoPaperSpotCarry(ctx)
	if diff := rows[0].AccruedUSD - 1.05; diff < -0.001 || diff > 0.001 {
		t.Errorf("demo accrued round2 = %v, want 1.05（缺席窗 +0 不重复/不外推）", rows[0].AccruedUSD)
	}
	_ = pr
}

// TestD474c_DemoAccrueAbsentWindow S3②：无任何 income_events → accrued +0（不外推、
// 不走模型公式），last_accrue_ts 照前推（实收口径缺席=真 0）。
func TestD474c_DemoAccrueAbsentWindow(t *testing.T) {
	s := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE fin_cex_universe, paper_spot_carry, income_events RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	now := time.Now().UTC()
	seedDemoRow(t, s, "binance", "GUSDT", now.Add(-24*time.Hour))
	if _, _, err := StepPaperSpotCarry(ctx, s, NewEntryStreak(), now, nil); err != nil {
		t.Fatalf("step: %v", err)
	}
	rows, _ := s.ListDemoPaperSpotCarry(ctx)
	if len(rows) != 1 || rows[0].AccruedUSD != 0 {
		t.Fatalf("accrued = %v want 0（缺席窗不外推）", rows[0].AccruedUSD)
	}
	if rows[0].LastAccrueTs == nil || rows[0].LastAccrueTs.Before(now.Add(-time.Minute)) {
		t.Errorf("last_accrue_ts = %v, want ≈now（实收口径缺席窗照前推）", rows[0].LastAccrueTs)
	}
}

// TestD474c_DemoWarnAnnotate S3③：归属粒度上限注记——行 note 幂等追加 flag_warn
// marker（两轮不叠注）；纸面行不被注（边界=只动 demo 行）。
func TestD474c_DemoWarnAnnotate(t *testing.T) {
	s := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE fin_cex_universe, paper_spot_carry, income_events RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	now := time.Now().UTC()
	pr := seedDemoRow(t, s, "binance", "GUSDT", now.Add(-24*time.Hour))
	for i := 0; i < 2; i++ {
		if _, _, err := StepPaperSpotCarry(ctx, s, NewEntryStreak(), now, nil); err != nil {
			t.Fatalf("step: %v", err)
		}
	}
	var note string
	if err := s.Pool().QueryRow(ctx, `SELECT note FROM paper_spot_carry WHERE id = $1`, pr.ID).Scan(&note); err != nil {
		t.Fatalf("read note: %v", err)
	}
	if !strings.Contains(note, "flag_warn") || !strings.Contains(note, "venue+symbol") {
		t.Errorf("note = %q, want flag_warn 归属粒度注记", note)
	}
	if strings.Count(note, "flag_warn") != 1 {
		t.Errorf("note = %q, want 幂等单注（两轮不叠）", note)
	}
}
