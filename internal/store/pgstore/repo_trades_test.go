// D-272 S8 测试：cnfi_repo_trades Store 方法（DSN 真实夹具，TRUNCATE 隔离）。
package pgstore

import (
	"context"
	"testing"
	"time"
)

// repoTradesTestStore 连接 ARB_TEST_PG_DSN 测试库，TRUNCATE cnfi_repo_trades 隔离数据。
func repoTradesTestStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	ctx := context.Background()
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, "TRUNCATE cnfi_repo_trades, cnfi_repo_ops, alerts, fin_repo_rates, cnfi_repo_config CASCADE"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	// cnfi_repo_config 单行表：TRUNCATE CASCADE 会删迁移插入的 id=1 行，重新插入。
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO cnfi_repo_config (id, idle_cny, min_return_cny, reminder_hour, enabled)
		VALUES (1, 50000, 5, 10, true) ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("reseed cnfi_repo_config: %v", err)
	}
	return s
}

// seedRepoRate 插入一条 fin_repo_rates 快照（理论利率抓取用）。
func seedRepoRate(t *testing.T, s *Store, code, name string, ratePct float64, tradeDate time.Time) {
	t.Helper()
	if err := s.InsertRepoRate(context.Background(), code, name, ratePct, nil, tradeDate); err != nil {
		t.Fatalf("InsertRepoRate: %v", err)
	}
}

// seedCalendar 插入日历数据（maturity_date 默认计算用）。
func seedCalendar(t *testing.T, s *Store, days map[string]bool) {
	t.Helper()
	ctx := context.Background()
	for day, biz := range days {
		d, _ := time.Parse("2006-01-02", day)
		if err := s.UpsertCNFICalendarDay(ctx, d, biz, "test"); err != nil {
			t.Fatalf("seed calendar %s: %v", day, err)
		}
	}
}

// TestRepoTrade_InsertAndList 插入 → ListRepoTrades 返回正确行。
func TestRepoTrade_InsertAndList(t *testing.T) {
	s := repoTradesTestStore(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	rate := 1.45
	theoReturn := 5.77
	devBps := 0.0
	devPrincipal := 350000.0
	snap := `{"instrument_code":"204001","rate_pct":1.45}`
	row := &RepoTradeRow{
		InstrumentCode:        "204001",
		TradeDate:             tradeDate,
		MaturityDate:          tradeDate.AddDate(0, 0, 1),
		HoldingDays:           3,
		PrincipalCny:          400000,
		ActualRatePct:         1.45,
		TheoreticalRatePct:    &rate,
		TheoreticalReturnCny:  &theoReturn,
		IdleCnyAtRecord:       &[]float64{50000}[0],
		SignalSnapshot:        &snap,
		DeviationRateBps:      &devBps,
		DeviationPrincipalCny: &devPrincipal,
	}
	id, err := s.InsertRepoTrade(ctx, row)
	if err != nil {
		t.Fatalf("InsertRepoTrade: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d, want > 0", id)
	}
	rows, err := s.ListRepoTrades(ctx, 10, "")
	if err != nil {
		t.Fatalf("ListRepoTrades: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len = %d, want 1", len(rows))
	}
	if rows[0].InstrumentCode != "204001" {
		t.Errorf("InstrumentCode = %q, want 204001", rows[0].InstrumentCode)
	}
	if rows[0].PrincipalCny != 400000 {
		t.Errorf("PrincipalCny = %v, want 400000", rows[0].PrincipalCny)
	}
	if rows[0].Status != "open" {
		t.Errorf("Status = %q, want open", rows[0].Status)
	}
	if rows[0].TheoreticalRatePct == nil || *rows[0].TheoreticalRatePct != 1.45 {
		t.Errorf("TheoreticalRatePct = %v, want 1.45", rows[0].TheoreticalRatePct)
	}
}

// TestRepoTrade_UniqueDateCode 同日同品种重复插入 → 唯一约束 error。
func TestRepoTrade_UniqueDateCode(t *testing.T) {
	s := repoTradesTestStore(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	row := &RepoTradeRow{
		InstrumentCode: "204001", TradeDate: tradeDate, MaturityDate: tradeDate.AddDate(0, 0, 1),
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
	}
	if _, err := s.InsertRepoTrade(ctx, row); err != nil {
		t.Fatalf("first InsertRepoTrade: %v", err)
	}
	if _, err := s.InsertRepoTrade(ctx, row); err == nil {
		t.Error("second InsertRepoTrade same date+code: expected unique constraint error, got nil")
	}
}

// TestRepoTrade_Settle 结算 → status=settled + 偏差计算。
func TestRepoTrade_Settle(t *testing.T) {
	s := repoTradesTestStore(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	theoReturn := 47.67
	row := &RepoTradeRow{
		InstrumentCode: "204001", TradeDate: tradeDate, MaturityDate: tradeDate.AddDate(0, 0, 1),
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
		TheoreticalReturnCny: &theoReturn,
	}
	id, err := s.InsertRepoTrade(ctx, row)
	if err != nil {
		t.Fatalf("InsertRepoTrade: %v", err)
	}
	devCny := 50.0 - theoReturn
	devPct := devCny / theoReturn * 100
	if err := s.SettleRepoTrade(ctx, id, 50.0, &devCny, &devPct); err != nil {
		t.Fatalf("SettleRepoTrade: %v", err)
	}
	trade, err := s.GetRepoTrade(ctx, id)
	if err != nil {
		t.Fatalf("GetRepoTrade: %v", err)
	}
	if trade.Status != "settled" {
		t.Errorf("Status = %q, want settled", trade.Status)
	}
	if trade.ActualReturnCny == nil || *trade.ActualReturnCny != 50.0 {
		t.Errorf("ActualReturnCny = %v, want 50.0", trade.ActualReturnCny)
	}
	if trade.DeviationReturnCny == nil || *trade.DeviationReturnCny != devCny {
		t.Errorf("DeviationReturnCny = %v, want %v", trade.DeviationReturnCny, devCny)
	}
}

// TestRepoTrade_Delete open 态可删；settled 态不可删。
func TestRepoTrade_Delete(t *testing.T) {
	s := repoTradesTestStore(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	// open 态
	row := &RepoTradeRow{
		InstrumentCode: "204001", TradeDate: tradeDate, MaturityDate: tradeDate.AddDate(0, 0, 1),
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
	}
	id, err := s.InsertRepoTrade(ctx, row)
	if err != nil {
		t.Fatalf("InsertRepoTrade: %v", err)
	}
	deleted, err := s.DeleteRepoTrade(ctx, id)
	if err != nil {
		t.Fatalf("DeleteRepoTrade open: %v", err)
	}
	if !deleted {
		t.Error("DeleteRepoTrade open = false, want true")
	}
	// 删后可重录
	if _, err := s.InsertRepoTrade(ctx, row); err != nil {
		t.Fatalf("re-insert after delete: %v", err)
	}

	// settled 态不可删
	id2, _ := s.InsertRepoTrade(ctx, &RepoTradeRow{
		InstrumentCode: "204007", TradeDate: tradeDate, MaturityDate: tradeDate.AddDate(0, 0, 1),
		HoldingDays: 3, PrincipalCny: 100000, ActualRatePct: 2.0,
	})
	if err := s.SettleRepoTrade(ctx, id2, 10.0, nil, nil); err != nil {
		t.Fatalf("SettleRepoTrade: %v", err)
	}
	deleted2, err := s.DeleteRepoTrade(ctx, id2)
	if err != nil {
		t.Fatalf("DeleteRepoTrade settled: %v", err)
	}
	if deleted2 {
		t.Error("DeleteRepoTrade settled = true, want false")
	}
}

// TestRepoTrade_ListMatured 到期未提醒的 open 记录被查出；已提醒的不查。
func TestRepoTrade_ListMatured(t *testing.T) {
	s := repoTradesTestStore(t)
	ctx := context.Background()
	pastDate := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)    // 到期日已过
	futureDate := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC) // 到期日未到
	// 到期 open
	id1, _ := s.InsertRepoTrade(ctx, &RepoTradeRow{
		InstrumentCode: "204001", TradeDate: pastDate.AddDate(0, 0, -3), MaturityDate: pastDate,
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
	})
	// 未到期 open
	s.InsertRepoTrade(ctx, &RepoTradeRow{
		InstrumentCode: "204007", TradeDate: futureDate.AddDate(0, 0, -3), MaturityDate: futureDate,
		HoldingDays: 3, PrincipalCny: 100000, ActualRatePct: 2.0,
	})
	today := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	matured, err := s.ListMaturedRepoTrades(ctx, today)
	if err != nil {
		t.Fatalf("ListMaturedRepoTrades: %v", err)
	}
	if len(matured) != 1 {
		t.Fatalf("len = %d, want 1", len(matured))
	}
	if matured[0].ID != id1 {
		t.Errorf("ID = %d, want %d", matured[0].ID, id1)
	}
	// 提醒后 → 不再查出
	if err := s.UpdateMaturityReminded(ctx, id1); err != nil {
		t.Fatalf("UpdateMaturityReminded: %v", err)
	}
	matured2, err := s.ListMaturedRepoTrades(ctx, today)
	if err != nil {
		t.Fatalf("ListMaturedRepoTrades after remind: %v", err)
	}
	if len(matured2) != 0 {
		t.Errorf("after remind len = %d, want 0", len(matured2))
	}
}

// TestRepoTrade_StrategyValidation 聚合统计正确。
func TestRepoTrade_StrategyValidation(t *testing.T) {
	s := repoTradesTestStore(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	// settled 行
	theoReturn := 47.67
	id1, _ := s.InsertRepoTrade(ctx, &RepoTradeRow{
		InstrumentCode: "204001", TradeDate: tradeDate, MaturityDate: tradeDate.AddDate(0, 0, 1),
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
		TheoreticalReturnCny: &theoReturn,
	})
	devCny := 2.33
	devPct := devCny / theoReturn * 100
	s.SettleRepoTrade(ctx, id1, 50.0, &devCny, &devPct)
	// open 行
	s.InsertRepoTrade(ctx, &RepoTradeRow{
		InstrumentCode: "204007", TradeDate: tradeDate, MaturityDate: tradeDate.AddDate(0, 0, 1),
		HoldingDays: 3, PrincipalCny: 100000, ActualRatePct: 2.0,
		TheoreticalReturnCny: &[]float64{16.44}[0],
	})
	v, err := s.GetRepoStrategyValidation(ctx)
	if err != nil {
		t.Fatalf("GetRepoStrategyValidation: %v", err)
	}
	if v.TotalTrades != 2 {
		t.Errorf("TotalTrades = %d, want 2", v.TotalTrades)
	}
	if v.SettledTrades != 1 {
		t.Errorf("SettledTrades = %d, want 1", v.SettledTrades)
	}
	if v.OpenTrades != 1 {
		t.Errorf("OpenTrades = %d, want 1", v.OpenTrades)
	}
	if v.TotalPrincipalCny != 400000 {
		t.Errorf("TotalPrincipalCny = %v, want 400000", v.TotalPrincipalCny)
	}
	if v.TotalActualReturn != 50.0 {
		t.Errorf("TotalActualReturn = %v, want 50.0", v.TotalActualReturn)
	}
	if v.OpenPrincipalCny != 100000 {
		t.Errorf("OpenPrincipalCny = %v, want 100000", v.OpenPrincipalCny)
	}
	// 偏差率 = 累计偏差 / 累计理论 * 100
	expectedPct := devCny / theoReturn * 100
	if v.TotalDeviationPct != expectedPct {
		t.Errorf("TotalDeviationPct = %v, want %v", v.TotalDeviationPct, expectedPct)
	}
}

// TestRepoTrade_StrategyValidation_DivZero 理论收益=0 时偏差率=0（除零保护）。
func TestRepoTrade_StrategyValidation_DivZero(t *testing.T) {
	s := repoTradesTestStore(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	// settled 行，理论收益=NULL（非交易时段录入）
	id1, _ := s.InsertRepoTrade(ctx, &RepoTradeRow{
		InstrumentCode: "204001", TradeDate: tradeDate, MaturityDate: tradeDate.AddDate(0, 0, 1),
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
	})
	s.SettleRepoTrade(ctx, id1, 50.0, nil, nil)
	v, err := s.GetRepoStrategyValidation(ctx)
	if err != nil {
		t.Fatalf("GetRepoStrategyValidation: %v", err)
	}
	if v.TotalDeviationPct != 0 {
		t.Errorf("TotalDeviationPct = %v, want 0 (除零保护)", v.TotalDeviationPct)
	}
}

// D-276 测试：手续费四品种费率计算正确。
func TestRepoTrade_FeeRates(t *testing.T) {
	s := repoTradesTestStore(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		code        string
		principal   float64
		expectedFee float64
	}{
		{"204001", 407000, 4.07}, // 407000 × 0.00001
		{"204007", 100000, 5.0},  // 100000 × 0.00005
		{"204014", 100000, 10.0}, // 100000 × 0.00010
		{"204028", 100000, 20.0}, // 100000 × 0.00020
	}
	for i, c := range cases {
		row := &RepoTradeRow{
			InstrumentCode: c.code, TradeDate: tradeDate.AddDate(0, 0, i),
			MaturityDate: tradeDate.AddDate(0, 0, i+1),
			HoldingDays:  1, PrincipalCny: c.principal, ActualRatePct: 1.45,
			FeeCny: &c.expectedFee,
		}
		id, err := s.InsertRepoTrade(ctx, row)
		if err != nil {
			t.Fatalf("case %d InsertRepoTrade: %v", i, err)
		}
		trade, err := s.GetRepoTrade(ctx, id)
		if err != nil {
			t.Fatalf("case %d GetRepoTrade: %v", i, err)
		}
		if trade.FeeCny == nil {
			t.Errorf("case %d FeeCny = nil, want %v", i, c.expectedFee)
			continue
		}
		if *trade.FeeCny != c.expectedFee {
			t.Errorf("case %d FeeCny = %v, want %v", i, *trade.FeeCny, c.expectedFee)
		}
	}
}

// D-276 测试：迁移回填——预插无 fee_cny 行 → Migrate → fee_cny 回填正确。
func TestRepoTrade_MigrateBackfillFee(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	// 清表避免唯一约束冲突
	if _, err := s.pool.Exec(ctx, "TRUNCATE cnfi_repo_trades"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 手动插入一行 fee_cny=NULL（模拟旧数据）
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO cnfi_repo_trades (instrument_code, trade_date, maturity_date, holding_days,
			principal_cny, actual_rate_pct, status, entry_source, fee_cny)
		VALUES ('204001', '2026-09-10', '2026-09-11', 1, 407000, 1.385, 'open', 'manual', NULL)`); err != nil {
		t.Fatalf("insert old row: %v", err)
	}
	// 手动执行回填 SQL（模拟迁移 0087 的 UPDATE 语句）
	if _, err := s.pool.Exec(ctx, `
		UPDATE cnfi_repo_trades SET fee_cny = principal_cny * CASE instrument_code
			WHEN '204001' THEN 0.00001 WHEN '204007' THEN 0.00005
			WHEN '204014' THEN 0.00010 WHEN '204028' THEN 0.00020 END
		WHERE fee_cny IS NULL`); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	// 验证回填值
	var fee float64
	err := s.pool.QueryRow(ctx, `SELECT fee_cny FROM cnfi_repo_trades WHERE instrument_code = '204001' AND principal_cny = 407000`).Scan(&fee)
	if err != nil {
		t.Fatalf("query fee: %v", err)
	}
	expected := 407000.0 * 0.00001
	if fee != expected {
		t.Errorf("backfilled fee_cny = %v, want %v", fee, expected)
	}
}

// D-276 测试：Validation 两新聚合（total_fee + total_net_theoretical）。
func TestRepoTrade_StrategyValidation_FeeAggregation(t *testing.T) {
	s := repoTradesTestStore(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	// settled 行：本金 407000，理论收益 46.33，手续费 4.07
	theoReturn := 46.33
	fee := 4.07
	id1, _ := s.InsertRepoTrade(ctx, &RepoTradeRow{
		InstrumentCode: "204001", TradeDate: tradeDate, MaturityDate: tradeDate.AddDate(0, 0, 1),
		HoldingDays: 1, PrincipalCny: 407000, ActualRatePct: 1.385,
		TheoreticalReturnCny: &theoReturn,
		FeeCny:               &fee,
	})
	// 净偏差 = actual - (theoretical - fee) = 42.26 - (46.33 - 4.07) = 42.26 - 42.26 = 0
	actualReturn := 42.26
	netTheo := theoReturn - fee
	devCny := actualReturn - netTheo // = 0
	devPct := 0.0
	s.SettleRepoTrade(ctx, id1, actualReturn, &devCny, &devPct)

	v, err := s.GetRepoStrategyValidation(ctx)
	if err != nil {
		t.Fatalf("GetRepoStrategyValidation: %v", err)
	}
	if v.TotalFeeCny != fee {
		t.Errorf("TotalFeeCny = %v, want %v", v.TotalFeeCny, fee)
	}
	expectedNet := theoReturn - fee
	if v.TotalNetTheoreticalReturn != expectedNet {
		t.Errorf("TotalNetTheoreticalReturn = %v, want %v", v.TotalNetTheoreticalReturn, expectedNet)
	}
}

// D-276 测试：理论 NULL 时 fee 仍写入，净列 NULL。
func TestRepoTrade_FeeWithNullTheoretical(t *testing.T) {
	s := repoTradesTestStore(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	// 理论为空（非交易时段），fee 仍写入
	fee := 4.07
	row := &RepoTradeRow{
		InstrumentCode: "204001", TradeDate: tradeDate, MaturityDate: tradeDate.AddDate(0, 0, 1),
		HoldingDays: 1, PrincipalCny: 407000, ActualRatePct: 1.385,
		FeeCny: &fee,
	}
	id, err := s.InsertRepoTrade(ctx, row)
	if err != nil {
		t.Fatalf("InsertRepoTrade: %v", err)
	}
	trade, err := s.GetRepoTrade(ctx, id)
	if err != nil {
		t.Fatalf("GetRepoTrade: %v", err)
	}
	if trade.FeeCny == nil || *trade.FeeCny != fee {
		t.Errorf("FeeCny = %v, want %v (理论为空但 fee 仍写入)", trade.FeeCny, fee)
	}
	if trade.TheoreticalReturnCny != nil {
		t.Errorf("TheoreticalReturnCny = %v, want nil (非交易时段)", trade.TheoreticalReturnCny)
	}
}
