package pgstore

import (
	"context"
	"testing"
	"time"
)

// d193SettlementTestStore 复用 testStore 并清空 settlements/positions（FK 链）。
func d193SettlementTestStore(t *testing.T) *Store {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE position_settlements, positions, position_legs, position_cashflows CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

// d193SetPos 建一条 CLOSED 持仓返回 id。
func d193SetPos(t *testing.T, s *Store, suffix string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1, 'SWAPARB', 'CLOSED', now()) RETURNING id`,
		"D193SET:SYM:"+suffix+":vA:vB").Scan(&id); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	return id
}

// TestInsertSettlement_D193Fields D-193-A S5：写入净 PNL 字段后可读回。
func TestInsertSettlement_D193Fields(t *testing.T) {
	s := d193SettlementTestStore(t)
	ctx := context.Background()
	posID := d193SetPos(t, s, "ins")
	pricePnl := 100.0
	cfNet := 3.0
	netReal := 103.0
	mv := 1
	notional := 50000.0
	r := &SettlementRow{
		PositionID:             posID,
		Vkey:                   "SWAPARB:vA:vB",
		ExpectedUsd:            50.0,
		RealizedUsd:            100.0,
		NotionalUsd:            &notional,
		HoldDays:               5.0,
		SkippedReason:          "",
		SettledAt:              time.Now(),
		PricePnlUsd:            &pricePnl,
		CashflowsNetUsd:        &cfNet,
		NetRealizedUsd:         &netReal,
		PnlQuality:             "actual",
		CompletenessStatus:     "complete",
		StatsStatus:            "applied",
		SettlementModelVersion: &mv,
		SettlementEventKey:     "settle:1",
	}
	won, err := s.InsertSettlement(ctx, r)
	if err != nil {
		t.Fatalf("InsertSettlement: %v", err)
	}
	if !won {
		t.Fatal("won=false, want true")
	}
	got, err := s.GetSettlementByPos(ctx, posID)
	if err != nil {
		t.Fatalf("GetSettlementByPos: %v", err)
	}
	if got == nil {
		t.Fatal("settlement not found")
	}
	if got.NetRealizedUsd == nil || !d193Approx(*got.NetRealizedUsd, 103.0, 0.001) {
		t.Errorf("net_realized_usd want 103.0, got %v", got.NetRealizedUsd)
	}
	if got.PricePnlUsd == nil || !d193Approx(*got.PricePnlUsd, 100.0, 0.001) {
		t.Errorf("price_pnl_usd want 100.0, got %v", got.PricePnlUsd)
	}
	if got.CashflowsNetUsd == nil || !d193Approx(*got.CashflowsNetUsd, 3.0, 0.001) {
		t.Errorf("cashflows_net_usd want 3.0, got %v", got.CashflowsNetUsd)
	}
	if got.PnlQuality != "actual" {
		t.Errorf("pnl_quality want actual, got %q", got.PnlQuality)
	}
	if got.CompletenessStatus != "complete" {
		t.Errorf("completeness_status want complete, got %q", got.CompletenessStatus)
	}
	if got.StatsStatus != "applied" {
		t.Errorf("stats_status want applied, got %q", got.StatsStatus)
	}
	if got.SettlementEventKey != "settle:1" {
		t.Errorf("settlement_event_key want settle:1, got %q", got.SettlementEventKey)
	}
}

// TestInsertSettlement_LegacyCompat D-193-A S5：旧字段 only（新字段零值）写入后读回不 panic。
func TestInsertSettlement_LegacyCompat(t *testing.T) {
	s := d193SettlementTestStore(t)
	ctx := context.Background()
	posID := d193SetPos(t, s, "leg")
	notional := 10000.0
	r := &SettlementRow{
		PositionID:    posID,
		Vkey:          "SWAPARB:vA:vB",
		ExpectedUsd:   50.0,
		RealizedUsd:   30.0,
		NotionalUsd:   &notional,
		HoldDays:      5.5,
		SkippedReason: "",
		SettledAt:     time.Now(),
		// D-193-A 字段全零值 → 写 NULL
	}
	won, err := s.InsertSettlement(ctx, r)
	if err != nil {
		t.Fatalf("InsertSettlement: %v", err)
	}
	if !won {
		t.Fatal("won=false, want true")
	}
	got, err := s.GetSettlementByPos(ctx, posID)
	if err != nil {
		t.Fatalf("GetSettlementByPos: %v", err)
	}
	if got == nil {
		t.Fatal("settlement not found")
	}
	if !d193Approx(got.RealizedUsd, 30.0, 0.001) {
		t.Errorf("realized_usd want 30.0, got %.4f", got.RealizedUsd)
	}
	if got.NetRealizedUsd != nil {
		t.Errorf("legacy net_realized_usd want nil, got %v", *got.NetRealizedUsd)
	}
	if got.PnlQuality != "" {
		t.Errorf("legacy pnl_quality want empty, got %q", got.PnlQuality)
	}
}

// TestGetSettlementByPos_D193Fields D-193-A S5：新字段可读（覆盖 GetSettlementByPos 路径）。
func TestGetSettlementByPos_D193Fields(t *testing.T) {
	s := d193SettlementTestStore(t)
	ctx := context.Background()
	posID := d193SetPos(t, s, "get")
	pricePnl := -5.0
	cfNet := 2.0
	netReal := -3.0
	r := &SettlementRow{
		PositionID:         posID,
		Vkey:               "SWAPARB:vA:vB",
		ExpectedUsd:        10.0,
		RealizedUsd:        -5.0,
		HoldDays:           1.0,
		SkippedReason:      "",
		SettledAt:          time.Now(),
		PricePnlUsd:        &pricePnl,
		CashflowsNetUsd:    &cfNet,
		NetRealizedUsd:     &netReal,
		PnlQuality:         "estimated",
		CompletenessStatus: "complete",
		StatsStatus:        "skipped",
	}
	if _, err := s.InsertSettlement(ctx, r); err != nil {
		t.Fatalf("InsertSettlement: %v", err)
	}
	got, err := s.GetSettlementByPos(ctx, posID)
	if err != nil {
		t.Fatalf("GetSettlementByPos: %v", err)
	}
	if got.PnlQuality != "estimated" {
		t.Errorf("pnl_quality want estimated, got %q", got.PnlQuality)
	}
	if got.StatsStatus != "skipped" {
		t.Errorf("stats_status want skipped, got %q", got.StatsStatus)
	}
}

// TestListSettlementsByPosIDs_D193Fields D-193-A S5：批量读新字段。
func TestListSettlementsByPosIDs_D193Fields(t *testing.T) {
	s := d193SettlementTestStore(t)
	ctx := context.Background()
	id1 := d193SetPos(t, s, "lst1")
	id2 := d193SetPos(t, s, "lst2")
	net1 := 50.0
	r1 := &SettlementRow{
		PositionID:         id1,
		Vkey:               "SWAPARB:vA:vB",
		ExpectedUsd:        10.0,
		RealizedUsd:        50.0,
		HoldDays:           2.0,
		SettledAt:          time.Now(),
		NetRealizedUsd:     &net1,
		PnlQuality:         "actual",
		CompletenessStatus: "complete",
		StatsStatus:        "applied",
	}
	if _, err := s.InsertSettlement(ctx, r1); err != nil {
		t.Fatalf("insert r1: %v", err)
	}
	// id2 插 legacy 行（直接 SQL，无 D-193 字段）
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at)
		 VALUES ($1, 'SWAPARB:vA:vB', 5.0, 20.0, 1.0, now())`, id2); err != nil {
		t.Fatalf("insert legacy r2: %v", err)
	}
	got, err := s.ListSettlementsByPosIDs(ctx, []int64{id1, id2})
	if err != nil {
		t.Fatalf("ListSettlementsByPosIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	sr1 := got[id1]
	if sr1.NetRealizedUsd == nil || !d193Approx(*sr1.NetRealizedUsd, 50.0, 0.001) {
		t.Errorf("id1 net_realized_usd want 50.0, got %v", sr1.NetRealizedUsd)
	}
	if sr1.PnlQuality != "actual" {
		t.Errorf("id1 pnl_quality want actual, got %q", sr1.PnlQuality)
	}
	sr2 := got[id2]
	if sr2.NetRealizedUsd != nil {
		t.Errorf("id2 legacy net_realized_usd want nil, got %v", *sr2.NetRealizedUsd)
	}
	if sr2.PnlQuality != "" {
		t.Errorf("id2 legacy pnl_quality want empty, got %q", sr2.PnlQuality)
	}
}

// TestGetSettlementStatsStatus D-193-A S5：stats_status 短路判定读取。
func TestGetSettlementStatsStatus(t *testing.T) {
	s := d193SettlementTestStore(t)
	ctx := context.Background()
	posID := d193SetPos(t, s, "stat")
	// 无行 → ""
	st, err := s.GetSettlementStatsStatus(ctx, posID)
	if err != nil {
		t.Fatalf("no row: %v", err)
	}
	if st != "" {
		t.Errorf("no row stats_status want empty, got %q", st)
	}
	// 插 applied 行
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, stats_status)
		 VALUES ($1, 'SWAPARB:vA:vB', 5.0, 20.0, 1.0, now(), 'applied')`, posID); err != nil {
		t.Fatalf("insert applied: %v", err)
	}
	st, err = s.GetSettlementStatsStatus(ctx, posID)
	if err != nil {
		t.Fatalf("applied row: %v", err)
	}
	if st != "applied" {
		t.Errorf("applied stats_status want applied, got %q", st)
	}
}

// TestMarkSettlementStatsRetryable D-193-A S5：改 stats_status=retryable。
func TestMarkSettlementStatsRetryable(t *testing.T) {
	s := d193SettlementTestStore(t)
	ctx := context.Background()
	posID := d193SetPos(t, s, "retry")
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, stats_status)
		 VALUES ($1, 'SWAPARB:vA:vB', 5.0, 20.0, 1.0, now(), 'applied')`, posID); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.MarkSettlementStatsRetryable(ctx, posID); err != nil {
		t.Fatalf("MarkSettlementStatsRetryable: %v", err)
	}
	st, err := s.GetSettlementStatsStatus(ctx, posID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if st != "retryable" {
		t.Errorf("stats_status want retryable, got %q", st)
	}
}

// TestAggregateStrategyStats_NetRealizedUsd D-193-A S6：新样本读 net_realized_usd，legacy 行 fallback realized_usd。
func TestAggregateStrategyStats_NetRealizedUsd(t *testing.T) {
	s := d193SettlementTestStore(t)
	ctx := context.Background()
	// d193AggPos 建带 closed_at + realized_pnl 的 CLOSED 持仓（AggregateStrategyStats WHERE 需要）。
	d193AggPos := func(suffix string, realized float64) int64 {
		t.Helper()
		var id int64
		if err := s.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl)
			 VALUES ($1, 'SWAPARB', 'CLOSED', now()-interval '7 days', now(), $2) RETURNING id`,
			"D193AGG:SYM:"+suffix, realized).Scan(&id); err != nil {
			t.Fatalf("insert agg pos: %v", err)
		}
		return id
	}
	// 新样本行：net_realized_usd=50（price 40 + cf 10），realized_usd=40（legacy 兼容列）
	id1 := d193AggPos("agg1", 40.0)
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd, price_pnl_usd, cashflows_net_usd)
		 VALUES ($1, 'SWAPARB:vA:vB', 10.0, 40.0, 1.0, now(), 50.0, 40.0, 10.0)`, id1); err != nil {
		t.Fatalf("insert new: %v", err)
	}
	// legacy 行：无 net_realized_usd，只有 realized_usd=20 → fallback 20
	id2 := d193AggPos("agg2", 20.0)
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at)
		 VALUES ($1, 'SWAPARB:vA:vB', 5.0, 20.0, 1.0, now())`, id2); err != nil {
		t.Fatalf("insert legacy: %v", err)
	}
	// 亏损行：net_realized_usd=-30
	id3 := d193AggPos("agg3", -30.0)
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd)
		 VALUES ($1, 'SWAPARB:vA:vB', 10.0, -30.0, 1.0, now(), -30.0)`, id3); err != nil {
		t.Fatalf("insert loss: %v", err)
	}
	agg, err := s.AggregateStrategyStats(ctx, "SWAPARB")
	if err != nil {
		t.Fatalf("AggregateStrategyStats: %v", err)
	}
	// net = 50 + 20 + (-30) = 40；total=3；wins=2（50>0, 20>0, -30<0）
	if agg.Total != 3 {
		t.Errorf("total=%d, want 3", agg.Total)
	}
	if agg.Wins != 2 {
		t.Errorf("wins=%d, want 2", agg.Wins)
	}
	// avg = 40/3 ≈ 13.33
	if !d193Approx(agg.AvgPnl, 40.0/3.0, 0.01) {
		t.Errorf("avg_pnl=%.4f, want ≈13.33", agg.AvgPnl)
	}
}
