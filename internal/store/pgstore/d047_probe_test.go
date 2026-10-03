package pgstore

import (
	"context"
	"testing"
	"time"
)

// d047ProbeStore 测试库 + 清干净探针相关表（testStore 只清指定两表，本票仓域自理）。
func d047ProbeStore(t *testing.T) *Store {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	for _, q := range []string{
		`TRUNCATE position_cashflows, position_settlements, position_legs, positions CASCADE`,
		`DELETE FROM app_config WHERE key LIKE 'ARB_PROBE%'`,
	} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatalf("clean: %v", err)
		}
	}
	return s
}

// d047SeedProbe 落一仓 type='PROBE'（参数化 status/closed/realized/auto_close_at）。
func d047SeedProbe(t *testing.T, s *Store, status, decisionRef string, realizedPnl *float64, autoClose *time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := s.CreatePosition(ctx, &Position{
		OpportunityID: "PROBE:XAUUSD:" + decisionRef,
		Type:          PositionTypePROBE,
		Status:        status,
		DecisionRef:   &decisionRef,
		AutoCloseAt:   autoClose,
	})
	if err != nil {
		t.Fatalf("create probe: %v", err)
	}
	if status == "CLOSED" || status == "FAILED" {
		if _, err := s.pool.Exec(ctx,
			`UPDATE positions SET closed_at=now(), realized_pnl=$1, exit_kind=$2 WHERE id=$3`,
			realizedPnl, ExitKindProbeExpired, id); err != nil {
			t.Fatalf("close mark: %v", err)
		}
	}
	return id
}

func d047OpenLeg(t *testing.T, s *Store, posID int64, venueKey, symbol, side, orderID string, qty, price float64) {
	t.Helper()
	now := time.Now()
	if err := s.CreatePositionLeg(context.Background(), &PositionLeg{
		PositionID: posID, LegNo: 1, Phase: "OPEN",
		Venue: venueKey, Account: "t", Symbol: symbol, Side: side, Qty: qty,
		PriceActual: &price, OrderID: orderID, Status: "FILLED",
		AttemptNo: 1, FilledQty: &qty, AvgFillPrice: &price, SubmittedAt: &now, Ts: now,
	}); err != nil {
		t.Fatalf("open leg: %v", err)
	}
}

// TestD047_DueAndList 到期扫描/在途计数/ListProbes 三读面。
func TestD047_DueAndList(t *testing.T) {
	s := d047ProbeStore(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	due := d047SeedProbe(t, s, "OPEN", "d047-a", nil, &past)
	d047OpenLeg(t, s, due, "tbinance:perp", "BTC-USDT", "BUY", "o-due", 0.01, 60000)
	notDue := d047SeedProbe(t, s, "OPEN", "d047-b", nil, &future)
	d047OpenLeg(t, s, notDue, "tbinance:perp", "BTC-USDT", "BUY", "o-nd", 0.01, 60000)
	d047SeedProbe(t, s, "CLOSED", "d047-c", nil, &past) // 已平不取

	rows, err := s.ListDueProbes(ctx, time.Now())
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != due || rows[0].VenueKey != "tbinance:perp" || rows[0].OrderID != "o-due" {
		t.Fatalf("due rows=%+v", rows)
	}
	if n, _ := s.CountActiveProbes(ctx); n != 2 {
		t.Fatalf("active=%d, want 2", n)
	}
	all, err := s.ListProbes(ctx, false)
	if err != nil || len(all) != 3 {
		t.Fatalf("list all=%d err=%v", len(all), err)
	}
	open, _ := s.ListProbes(ctx, true)
	if len(open) != 2 {
		t.Fatalf("list open=%d, want 2", len(open))
	}
	if all[0].DecisionRef == nil || *all[0].DecisionRef == "" {
		t.Fatal("decision_ref 未回填")
	}
}

// TestD047_StatsExclusion PROBE 排除：NetRealizedSince/DailyRealizedPnl/GetStrategyAgg
// 三统计面一律不计；账本现金流仍可入账（ProbeCashflowSumSince 单列）。
func TestD047_StatsExclusion(t *testing.T) {
	s := d047ProbeStore(t)
	ctx := context.Background()
	since := time.Now().Add(-time.Hour)

	probeLoss := -12.34
	probeID := d047SeedProbe(t, s, "CLOSED", "d047-x", &probeLoss, nil)
	d047OpenLeg(t, s, probeID, "tbinance:perp", "BTC-USDT", "BUY", "o-x", 0.01, 60000)
	// 对照：普通 FUNDING_HEDGE 已平仓（不剔）——验证排除只作用于 PROBE。
	normPnL := 5.0
	nID, err := s.CreatePosition(ctx, &Position{OpportunityID: "FH:t", Type: "FUNDING_HEDGE", Status: "CLOSED"})
	if err != nil {
		t.Fatalf("create normal: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE positions SET closed_at=now(), realized_pnl=$1 WHERE id=$2`, normPnL, nID); err != nil {
		t.Fatalf("close normal: %v", err)
	}

	// 探针现金流入手写一条（负=成本）——账本收、统计不收。
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO position_cashflows (position_id, venue, kind, amount_usd, ts) VALUES ($1,'tbinance','commission',-0.5,now())`, probeID); err != nil {
		t.Fatalf("cf: %v", err)
	}

	if net, err := s.NetRealizedSince(ctx, since, false); err != nil || net != normPnL {
		t.Errorf("NetRealizedSince=%.2f err=%v, want %.2f（PROBE −12.34 剔除）", net, err, normPnL)
	}
	if day, err := s.DailyRealizedPnl(ctx); err != nil || day != normPnL {
		t.Errorf("DailyRealizedPnl=%.2f err=%v, want %.2f", day, err, normPnL)
	}
	aggs, err := s.GetStrategyAgg(ctx, "")
	if err != nil {
		t.Fatalf("agg: %v", err)
	}
	for _, a := range aggs {
		if a.Strategy == PositionTypePROBE {
			t.Errorf("GetStrategyAgg 含 PROBE 策略行（%d 仓 $%.2f）", a.TotalPositions, a.RealizedPnlUsd)
		}
	}
	// 账本面：现金流求和只收 PROBE（腿级 commission 行 + 手写行）。
	sum, err := s.ProbeCashflowSumSince(ctx, since)
	if err != nil {
		t.Fatalf("probe cf: %v", err)
	}
	if sum > -0.4 || sum < -1.5 {
		t.Errorf("ProbeCashflowSumSince=%.4f, want ≈−0.5（含腿 commission 0）", sum)
	}
}

// TestD047_ProbeMeta broker 持仓页标注源。
func TestD047_ProbeMeta(t *testing.T) {
	s := d047ProbeStore(t)
	ctx := context.Background()
	at := time.Now().Add(2 * time.Hour)
	pid := d047SeedProbe(t, s, "OPEN", "d047-meta", nil, &at)
	meta, err := s.ProbeMetaByPositionIDs(ctx, []int64{pid, 999999})
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	m, ok := meta[pid]
	if !ok || m.DecisionRef != "d047-meta" || m.AutoCloseAt == nil {
		t.Fatalf("meta=%+v", meta)
	}
}
