package pgstore

import (
	"context"
	"testing"
	"time"

	"arb/internal/capital"
	"arb/internal/testutil"
)

// D-193-C：Hypothesis/Decision/Drawdown/Review/Report 持久化测试（DSN-gated）。
// 复用 prepareTestSchema + testutil.TestDSN 模式。

func testCapitalStoreC(t *testing.T) *Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, `TRUNCATE capital_accounts, capital_account_venue_links, capital_snapshots, capital_decisions, hypotheses, position_reviews, ledger, position_settlements, positions CASCADE`); err != nil {
		t.Fatalf("truncate capital tables: %v", err)
	}
	return s
}

func fptrC(v float64) *float64 { return &v }
func sptrC(v string) *string   { return &v }
func iptrC(v int) *int         { return &v }

// === S7 测试 ===

// TestUpsertHypothesis_Create：创建假设可读回。
func TestUpsertHypothesis_Create(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	h := &capital.Hypothesis{
		HypothesisID:      "HYP-TEST-001",
		HypothesisVersion: 1,
		Status:            "approved",
		NetReturnHurdle:   8.0,
		MaxDrawdown:       10.0,
		PrincipalRule:     "本金保护：surplus<0 停止",
		KillRule:          "年化<6% 证伪",
	}
	if err := s.UpsertHypothesis(ctx, h); err != nil {
		t.Fatalf("UpsertHypothesis: %v", err)
	}
	got, err := s.GetHypothesis(ctx, "HYP-TEST-001")
	if err != nil {
		t.Fatalf("GetHypothesis: %v", err)
	}
	if got.Status != "approved" {
		t.Errorf("status=%q, want approved", got.Status)
	}
	if got.NetReturnHurdle != 8.0 {
		t.Errorf("net_return_hurdle=%v, want 8.0", got.NetReturnHurdle)
	}
}

// TestAppendCapitalDecision_Idempotent：同 event_key 二次追加 won=false。
func TestAppendCapitalDecision_Idempotent(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "dec-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	d := &capital.CapitalDecision{
		AccountID:  &acc.AccountID,
		Decision:   "allocate",
		Reason:     "test allocate",
		EventKey:   "dec-evt-001",
		OccurredAt: time.Now(),
	}
	won1, err := s.AppendCapitalDecision(ctx, d)
	if err != nil {
		t.Fatalf("first AppendCapitalDecision: %v", err)
	}
	if !won1 {
		t.Errorf("first won=false, want true (new insert)")
	}
	won2, err := s.AppendCapitalDecision(ctx, d)
	if err != nil {
		t.Fatalf("second AppendCapitalDecision: %v", err)
	}
	if won2 {
		t.Errorf("second won=true, want false (idempotent, already exists)")
	}
}

// TestListCapitalDecisions_Ordering：时间倒序。
func TestListCapitalDecisions_Ordering(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "list-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	// 插 3 个决策，occurred_at 递增
	for i, ts := range []string{"2026-01-01", "2026-01-02", "2026-01-03"} {
		d := &capital.CapitalDecision{
			AccountID:  &acc.AccountID,
			Decision:   "wait",
			Reason:     "test",
			EventKey:   "list-evt-" + string(rune('0'+i)),
			OccurredAt: mustParseTime(ts),
		}
		if _, err := s.AppendCapitalDecision(ctx, d); err != nil {
			t.Fatalf("AppendCapitalDecision %d: %v", i, err)
		}
	}
	list, err := s.ListCapitalDecisions(ctx, acc.AccountID, 10)
	if err != nil {
		t.Fatalf("ListCapitalDecisions: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("list len=%d, want 3", len(list))
	}
	// 最新在前
	first := list[0].OccurredAt.Format("2006-01-02")
	if first != "2026-01-03" {
		t.Errorf("first occurred_at date=%s, want 2026-01-03 (desc order)", first)
	}
}

// TestDrawdown_HWM：多快照 HWM 正确。
func TestDrawdown_HWM(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "dd-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	// 插 3 个 actual 快照，liquidatable_equity 递增后降
	for i, eq := range []float64{1000, 1200, 1100} {
		ts := time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC)
		snap := capital.CapitalSnapshot{
			ScopeType:             "account",
			ScopeKey:              "dd-acc",
			AccountID:             &acc.AccountID,
			Source:                "broker",
			SourceTS:              &ts,
			LiquidatableEquityUSD: fptrC(eq),
			Quality:               "actual",
		}
		if err := s.AppendCapitalSnapshot(ctx, &snap); err != nil {
			t.Fatalf("AppendCapitalSnapshot %d: %v", i, err)
		}
	}
	dd, err := s.Drawdown(ctx, acc.AccountID, time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Drawdown: %v", err)
	}
	// HWM=1200, current=1100 → DD=1-1100/1200=0.0833
	if dd.HWM != 1200 {
		t.Errorf("HWM=%v, want 1200", dd.HWM)
	}
	if dd.CurrentEquity != 1100 {
		t.Errorf("CurrentEquity=%v, want 1100", dd.CurrentEquity)
	}
	wantDD := 1 - 1100.0/1200.0
	if absF(dd.DrawdownPct-wantDD) > 1e-6 {
		t.Errorf("DrawdownPct=%v, want %v", dd.DrawdownPct, wantDD)
	}
}

// TestDrawdown_UnknownSnapshot_Skipped（D-193 审计 P1-10）：quality=unknown 快照被跳过，不报错；actual 快照正常计算。
func TestDrawdown_UnknownSnapshot_Skipped(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "dd-skip-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	// 插 1 个 unknown + 1 个 actual
	ts1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	// unknown 快照（应被跳过）
	snapUnk := capital.CapitalSnapshot{
		ScopeType: "account", ScopeKey: "dd-skip-acc", AccountID: &acc.AccountID,
		Source: "broker", SourceTS: &ts1,
		LiquidatableEquityUSD: fptrC(9999), Quality: "unknown",
	}
	if err := s.AppendCapitalSnapshot(ctx, &snapUnk); err != nil {
		t.Fatalf("AppendCapitalSnapshot unknown: %v", err)
	}
	// actual 快照
	snapAct := capital.CapitalSnapshot{
		ScopeType: "account", ScopeKey: "dd-skip-acc", AccountID: &acc.AccountID,
		Source: "broker", SourceTS: &ts2,
		LiquidatableEquityUSD: fptrC(1000), Quality: "actual",
	}
	if err := s.AppendCapitalSnapshot(ctx, &snapAct); err != nil {
		t.Fatalf("AppendCapitalSnapshot actual: %v", err)
	}
	dd, err := s.Drawdown(ctx, acc.AccountID, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Drawdown: %v (unknown should be skipped, not error)", err)
	}
	// HWM=1000（unknown 9999 被跳过），current=1000 → DD=0
	if dd.HWM != 1000 {
		t.Errorf("HWM=%v, want 1000 (unknown 9999 skipped)", dd.HWM)
	}
	if dd.DrawdownPct != 0 {
		t.Errorf("DrawdownPct=%v, want 0", dd.DrawdownPct)
	}
}

// TestDrawdown_AllUnknown_ErrUnknownFact（D-193 审计 P1-10）：全部快照 unknown → ErrUnknownFact。
func TestDrawdown_AllUnknown_ErrUnknownFact(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "dd-allunk-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snap := capital.CapitalSnapshot{
		ScopeType: "account", ScopeKey: "dd-allunk-acc", AccountID: &acc.AccountID,
		Source: "broker", SourceTS: &ts,
		LiquidatableEquityUSD: fptrC(1000), Quality: "unknown",
	}
	if err := s.AppendCapitalSnapshot(ctx, &snap); err != nil {
		t.Fatalf("AppendCapitalSnapshot: %v", err)
	}
	_, err = s.Drawdown(ctx, acc.AccountID, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("Drawdown err=nil, want ErrUnknownFact (all unknown)")
	}
	if err != capital.ErrUnknownFact {
		t.Errorf("err=%v, want ErrUnknownFact", err)
	}
}

// TestOverallDrawdown：overall scope 正确。
func TestOverallDrawdown(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	// 插 overall 快照
	for i, eq := range []float64{2000, 2400, 2200} {
		ts := time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC)
		snap := capital.CapitalSnapshot{
			ScopeType:             "overall",
			ScopeKey:              "portfolio",
			Source:                "aggregator",
			SourceTS:              &ts,
			LiquidatableEquityUSD: fptrC(eq),
			Quality:               "actual",
		}
		if err := s.AppendCapitalSnapshot(ctx, &snap); err != nil {
			t.Fatalf("AppendCapitalSnapshot %d: %v", i, err)
		}
	}
	dd, err := s.OverallDrawdown(ctx, nil, time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("OverallDrawdown: %v", err)
	}
	if dd.HWM != 2400 {
		t.Errorf("HWM=%v, want 2400", dd.HWM)
	}
	if dd.CurrentEquity != 2200 {
		t.Errorf("CurrentEquity=%v, want 2200", dd.CurrentEquity)
	}
}

// TestUpsertPositionReview：创建/更新复盘。
func TestUpsertPositionReview(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	// 需要先建 position（position_reviews FK 引用 positions.id）
	posID, err := s.pool.Exec(ctx, `INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ('rev-opp', 'SWAPARB', 'CLOSED', now()) RETURNING id`)
	if err != nil {
		t.Fatalf("insert position: %v", err)
	}
	_ = posID
	var pid int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM positions WHERE opportunity_id = 'rev-opp'`).Scan(&pid); err != nil {
		t.Fatalf("get position id: %v", err)
	}
	r := &PositionReview{
		PositionID:       pid,
		ExpectedNetUSD:   fptrC(10),
		ActualNetUSD:     fptrC(8),
		CostDeviationUSD: fptrC(-2),
		FollowupAction:   sptrC("continue"),
		ReviewNote:       sptrC("test review"),
	}
	if err := s.UpsertPositionReview(ctx, r); err != nil {
		t.Fatalf("UpsertPositionReview: %v", err)
	}
	// 更新（ON CONFLICT position_id DO UPDATE）
	r.ActualNetUSD = fptrC(9)
	if err := s.UpsertPositionReview(ctx, r); err != nil {
		t.Fatalf("second UpsertPositionReview (update): %v", err)
	}
}

// === S8 测试 ===

// TestGetAccountPerformance_NoData：无快照/无结算 → CurrentEquity=nil, Quality='unknown'。
func TestGetAccountPerformance_NoData(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "rpt-nodata-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	rpt, err := s.GetAccountPerformance(ctx, acc.AccountID)
	if err != nil {
		t.Fatalf("GetAccountPerformance: %v", err)
	}
	if rpt.CurrentEquity != nil {
		t.Errorf("CurrentEquity=%v, want nil (no data)", rpt.CurrentEquity)
	}
	if rpt.Quality != "unknown" {
		t.Errorf("Quality=%q, want unknown (no data)", rpt.Quality)
	}
}

// TestGetAccountPerformance_WithSettlements：有 actual+complete+applied 结算 → NetRealizedUSD 正确。
func TestGetAccountPerformance_WithSettlements(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "rpt-settle-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	// 建 position + settlement + decision（关联 opportunity_id）
	_, err = s.pool.Exec(ctx, `INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ('rpt-settle-opp', 'SWAPARB', 'CLOSED', now())`)
	if err != nil {
		t.Fatalf("insert position: %v", err)
	}
	var posID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM positions WHERE opportunity_id = 'rpt-settle-opp'`).Scan(&posID); err != nil {
		t.Fatalf("get position id: %v", err)
	}
	// settlement：四重 AND 全满足
	_, err = s.pool.Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, net_realized_usd, pnl_quality, completeness_status, stats_status)
		 VALUES ($1, 'vkey1', 10, 8, 8, 'actual', 'complete', 'applied')`, posID)
	if err != nil {
		t.Fatalf("insert settlement: %v", err)
	}
	// decision 关联 opportunity_id
	d := &capital.CapitalDecision{
		AccountID:     &acc.AccountID,
		OpportunityID: sptrC("rpt-settle-opp"),
		Decision:      "allocate",
		Reason:        "test",
		EventKey:      "rpt-settle-dec",
		OccurredAt:    time.Now(),
	}
	if _, err := s.AppendCapitalDecision(ctx, d); err != nil {
		t.Fatalf("AppendCapitalDecision: %v", err)
	}
	rpt, err := s.GetAccountPerformance(ctx, acc.AccountID)
	if err != nil {
		t.Fatalf("GetAccountPerformance: %v", err)
	}
	if rpt.NetRealizedUSD == nil || *rpt.NetRealizedUSD != 8 {
		t.Errorf("NetRealizedUSD=%v, want 8", rpt.NetRealizedUSD)
	}
}

// TestGetAccountPerformance_StatsStatusFilter（D-193 审计 P1-11）：stats_status='retryable' 的结算不计入 NetRealizedUSD。
func TestGetAccountPerformance_StatsStatusFilter(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "rpt-filter-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	// position + settlement（stats_status='retryable'，应被过滤）
	_, err = s.pool.Exec(ctx, `INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ('rpt-filter-opp', 'SWAPARB', 'CLOSED', now())`)
	if err != nil {
		t.Fatalf("insert position: %v", err)
	}
	var posID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM positions WHERE opportunity_id = 'rpt-filter-opp'`).Scan(&posID); err != nil {
		t.Fatalf("get position id: %v", err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, net_realized_usd, pnl_quality, completeness_status, stats_status)
		 VALUES ($1, 'vkey2', 10, 8, 8, 'actual', 'complete', 'retryable')`, posID)
	if err != nil {
		t.Fatalf("insert settlement: %v", err)
	}
	d := &capital.CapitalDecision{
		AccountID:     &acc.AccountID,
		OpportunityID: sptrC("rpt-filter-opp"),
		Decision:      "allocate",
		Reason:        "test",
		EventKey:      "rpt-filter-dec",
		OccurredAt:    time.Now(),
	}
	if _, err := s.AppendCapitalDecision(ctx, d); err != nil {
		t.Fatalf("AppendCapitalDecision: %v", err)
	}
	rpt, err := s.GetAccountPerformance(ctx, acc.AccountID)
	if err != nil {
		t.Fatalf("GetAccountPerformance: %v", err)
	}
	// stats_status='retryable' → 不计入 NetRealizedUSD
	if rpt.NetRealizedUSD != nil {
		t.Errorf("NetRealizedUSD=%v, want nil (stats_status=retryable filtered)", *rpt.NetRealizedUSD)
	}
}

// TestGetActualEconomics_Normal（D-193 审计 P1-12）：complete+actual 行 → NetRealizedUSD 正确。
func TestGetActualEconomics_Normal(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ('ae-opp', 'SWAPARB', 'CLOSED', now())`)
	if err != nil {
		t.Fatalf("insert position: %v", err)
	}
	var posID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM positions WHERE opportunity_id = 'ae-opp'`).Scan(&posID); err != nil {
		t.Fatalf("get position id: %v", err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, net_realized_usd, pnl_quality, completeness_status, stats_status)
		 VALUES ($1, 'vkey3', 10, 8, 8, 'actual', 'complete', 'applied')`, posID)
	if err != nil {
		t.Fatalf("insert settlement: %v", err)
	}
	ae, err := s.GetActualEconomics(ctx, posID)
	if err != nil {
		t.Fatalf("GetActualEconomics: %v", err)
	}
	if ae.NetRealizedUSD == nil || *ae.NetRealizedUSD != 8 {
		t.Errorf("NetRealizedUSD=%v, want 8", ae.NetRealizedUSD)
	}
	if ae.PnlQuality != "actual" {
		t.Errorf("PnlQuality=%q, want actual", ae.PnlQuality)
	}
}

// TestGetActualEconomics_Legacy：legacy 行 → PnlQuality='legacy', NetRealizedUSD=nil。
func TestGetActualEconomics_Legacy(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ('ae-legacy-opp', 'SWAPARB', 'CLOSED', now())`)
	if err != nil {
		t.Fatalf("insert position: %v", err)
	}
	var posID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM positions WHERE opportunity_id = 'ae-legacy-opp'`).Scan(&posID); err != nil {
		t.Fatalf("get position id: %v", err)
	}
	// legacy 行：net_realized_usd IS NULL, pnl_quality='legacy'
	_, err = s.pool.Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, pnl_quality)
		 VALUES ($1, 'vkey4', 10, 5, 'legacy')`, posID)
	if err != nil {
		t.Fatalf("insert settlement: %v", err)
	}
	ae, err := s.GetActualEconomics(ctx, posID)
	if err != nil {
		t.Fatalf("GetActualEconomics: %v", err)
	}
	if ae.NetRealizedUSD != nil {
		t.Errorf("NetRealizedUSD=%v, want nil (legacy)", ae.NetRealizedUSD)
	}
	if ae.PnlQuality != "legacy" {
		t.Errorf("PnlQuality=%q, want legacy", ae.PnlQuality)
	}
}

// TestGetActualEconomics_NoSettlement：无行 → ErrUnknownFact。
func TestGetActualEconomics_NoSettlement(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ('ae-none-opp', 'SWAPARB', 'CLOSED', now())`)
	if err != nil {
		t.Fatalf("insert position: %v", err)
	}
	var posID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM positions WHERE opportunity_id = 'ae-none-opp'`).Scan(&posID); err != nil {
		t.Fatalf("get position id: %v", err)
	}
	_, err = s.GetActualEconomics(ctx, posID)
	if err == nil {
		t.Fatal("err=nil, want ErrUnknownFact (no settlement)")
	}
	if err != capital.ErrUnknownFact {
		t.Errorf("err=%v, want ErrUnknownFact", err)
	}
}

// TestListDecisionsForAccount_Ordering（D-193 审计 P1-13）：时间倒序。
func TestListDecisionsForAccount_Ordering(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "listdec-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	for i, ts := range []string{"2026-02-01", "2026-02-02", "2026-02-03"} {
		d := &capital.CapitalDecision{
			AccountID:  &acc.AccountID,
			Decision:   "wait",
			Reason:     "test",
			EventKey:   "listdec-evt-" + string(rune('0'+i)),
			OccurredAt: mustParseTime(ts),
		}
		if _, err := s.AppendCapitalDecision(ctx, d); err != nil {
			t.Fatalf("AppendCapitalDecision %d: %v", i, err)
		}
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	list, err := s.ListDecisionsForAccount(ctx, acc.AccountID, from, to, 10)
	if err != nil {
		t.Fatalf("ListDecisionsForAccount: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("list len=%d, want 3", len(list))
	}
	first := list[0].OccurredAt.Format("2006-01-02")
	if first != "2026-02-03" {
		t.Errorf("first occurred_at date=%s, want 2026-02-03 (desc)", first)
	}
}

// TestGetDecisionChain_FullChain（D-193 审计 P1-13）：decision→position→legs→cashflows→settlement 全链。
func TestGetDecisionChain_FullChain(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "chain-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	// position + legs + settlement
	_, err = s.pool.Exec(ctx, `INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ('chain-opp', 'SWAPARB', 'CLOSED', now())`)
	if err != nil {
		t.Fatalf("insert position: %v", err)
	}
	var posID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM positions WHERE opportunity_id = 'chain-opp'`).Scan(&posID); err != nil {
		t.Fatalf("get position id: %v", err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd, pnl_quality, completeness_status, stats_status)
		 VALUES ($1, 'vkey5', 10, 8, 1.0, now(), 8, 'actual', 'complete', 'applied')`, posID)
	if err != nil {
		t.Fatalf("insert settlement: %v", err)
	}
	d := &capital.CapitalDecision{
		AccountID:     &acc.AccountID,
		OpportunityID: sptrC("chain-opp"),
		Decision:      "allocate",
		Reason:        "test chain",
		EventKey:      "chain-dec-evt",
		OccurredAt:    time.Now(),
	}
	won, err := s.AppendCapitalDecision(ctx, d)
	if err != nil || !won {
		t.Fatalf("AppendCapitalDecision: won=%v err=%v", won, err)
	}
	var decID int64
	if err := s.pool.QueryRow(ctx, `SELECT decision_id FROM capital_decisions WHERE event_key = 'chain-dec-evt'`).Scan(&decID); err != nil {
		t.Fatalf("get decision_id: %v", err)
	}
	chain, err := s.GetDecisionChain(ctx, decID)
	if err != nil {
		t.Fatalf("GetDecisionChain: %v", err)
	}
	if chain.Decision == nil {
		t.Fatal("Decision=nil")
	}
	if chain.Position == nil {
		t.Fatal("Position=nil, want non-nil (opportunity_id=chain-opp exists)")
	}
	if chain.Settlement == nil {
		t.Fatal("Settlement=nil, want non-nil")
	}
}

// TestGetDecisionChain_NoOpportunity：decision 无 opportunity_id → Position=nil。
func TestGetDecisionChain_NoOpportunity(t *testing.T) {
	s := testCapitalStoreC(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "noopp-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	d := &capital.CapitalDecision{
		AccountID:  &acc.AccountID,
		Decision:   "wait",
		Reason:     "no opp",
		EventKey:   "noopp-dec-evt",
		OccurredAt: time.Now(),
	}
	won, err := s.AppendCapitalDecision(ctx, d)
	if err != nil || !won {
		t.Fatalf("AppendCapitalDecision: won=%v err=%v", won, err)
	}
	var decID int64
	if err := s.pool.QueryRow(ctx, `SELECT decision_id FROM capital_decisions WHERE event_key = 'noopp-dec-evt'`).Scan(&decID); err != nil {
		t.Fatalf("get decision_id: %v", err)
	}
	chain, err := s.GetDecisionChain(ctx, decID)
	if err != nil {
		t.Fatalf("GetDecisionChain: %v", err)
	}
	if chain.Decision == nil {
		t.Fatal("Decision=nil")
	}
	if chain.Position != nil {
		t.Errorf("Position=%v, want nil (no opportunity_id)", chain.Position)
	}
}

// 辅助函数
func mustParseTime(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
