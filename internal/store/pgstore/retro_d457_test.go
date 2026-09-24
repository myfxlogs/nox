package pgstore

import (
	"context"
	"testing"
	"time"

	"arb/internal/testutil"
)

// TestRetroStats_D457：日报/周报聚合 SQL 真库语法+语义冒烟（建最小行集验证计数）。
func TestRetroStats_D457(t *testing.T) {
	s, err := New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// 最小行：1 OPEN 仓+腿+funding 实收行；1 CLOSED 仓（realized>0）。
	var posID, closedID int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO positions (opportunity_id,type,status,expected_net_bps,opened_at)
		VALUES ('d457-a','XASSET_CARRY','OPEN',10,$1) RETURNING id`, now.Add(-2*time.Hour)).Scan(&posID); err != nil {
		t.Fatal(err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM positions WHERE opportunity_id LIKE 'd457-%'`)
	if err := s.pool.QueryRow(ctx, `INSERT INTO positions (opportunity_id,type,status,expected_net_bps,realized_pnl,opened_at,closed_at,notional_usd)
		VALUES ('d457-b','XASSET_CARRY','CLOSED',10,1.5,$1,$2,1000) RETURNING id`,
		now.Add(-3*24*time.Hour), now.Add(-time.Hour)).Scan(&closedID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO position_legs (position_id,leg_no,phase,status,venue,account,symbol,side,qty,price_expected)
		VALUES ($1,1,'OPEN','FILLED','gate','a','XAU','SELL',1,1)`, posID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO position_cashflows (position_id,kind,venue,amount_usd,ts,quality)
		VALUES ($1,'funding','gate',1.0313,$2,'actual')`, posID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM position_cashflows WHERE position_id IN ($1,$2)`, posID, closedID)
	if _, err := s.pool.Exec(ctx, `INSERT INTO position_evals (position_id,evaluated_at,decision) VALUES ($1,$2,'hold')`, posID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM position_evals WHERE position_id=$1`, posID)

	d, err := s.RetroDailyStats(ctx, start, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range d.OpenPos {
		if p.ID == posID {
			found = true
			if p.Venue != "gate" || p.CarryUSD != 1.0313 {
				t.Fatalf("open pos row wrong: %+v", p)
			}
		}
	}
	if !found {
		t.Fatalf("open pos %d missing: %+v", posID, d.OpenPos)
	}
	if d.OpenedToday < 1 || d.FundingActualUSD < 1.0313 || d.EvalDist["hold"] < 1 {
		t.Fatalf("daily stats wrong: %+v", d)
	}

	w, err := s.RetroWeeklyStats(ctx, now.Add(-7*24*time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var g *RetroClosedGroup
	for i := range w.Groups {
		if w.Groups[i].Type == "XASSET_CARRY" {
			g = &w.Groups[i]
		}
	}
	if g == nil || g.Wins < 1 {
		t.Fatalf("weekly groups wrong: %+v", w.Groups)
	}
	if w.TopWin == nil {
		t.Fatal("TopWin nil")
	}
}
