package pgstore

import (
	"context"
	"testing"
	"time"

	"arb/internal/platform/testutil"
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
	// 窗口=相对 now（非历法午夜）：原 start=当日 00:00 UTC 与插入行 now-2h 在 UTC 00:00–02:00 段
	// 跨日出窗→OpenedToday=0 假红（2026-09-25 00:55Z 机检实证）。测试目的=SQL 语法/语义冒烟，
	// 窗口参数任意；相对窗使断言墙钟无关。
	start := now.Add(-time.Hour)

	// 最小行：1 OPEN 仓+腿+funding 实收行；1 CLOSED 仓（realized>0）。
	var posID, closedID int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO positions (opportunity_id,type,status,expected_net_bps,opened_at)
		VALUES ('d457-a','XASSET_CARRY','OPEN',10,$1) RETURNING id`, now.Add(-30*time.Minute)).Scan(&posID); err != nil {
		t.Fatal(err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM positions WHERE opportunity_id LIKE 'd457-%'`)
	if err := s.pool.QueryRow(ctx, `INSERT INTO positions (opportunity_id,type,status,expected_net_bps,realized_pnl,opened_at,closed_at,notional_usd)
		VALUES ('d457-b','XASSET_CARRY','CLOSED',10,1.5,$1,$2,1000) RETURNING id`,
		now.Add(-3*24*time.Hour), now.Add(-30*time.Minute)).Scan(&closedID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO position_legs (position_id,leg_no,phase,status,venue,venue_type,symbol,side,qty,price_expected)
		VALUES ($1,1,'OPEN','FILLED','gate','perp','XAU','SELL',1,1)`, posID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO position_cashflows (position_id,kind,venue,venue_type,amount_usd,ts,quality)
		VALUES ($1,'funding','gate','perp',1.0313,$2,'actual')`, posID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM position_cashflows WHERE position_id IN ($1,$2)`, posID, closedID)
	if _, err := s.pool.Exec(ctx, `INSERT INTO position_evals (position_id,evaluated_at,decision) VALUES ($1,$2,'hold')`, posID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM position_evals WHERE position_id=$1`, posID)

	// 毕业建议窗口计数——插 paper 行（FK 父）+ carry_graduations 行（created_at=now 入窗）。
	if err := s.InsertPaperSpotCarry(ctx, &PaperSpotCarry{VenueA: "gate", Symbol: "D468P", Side: "short_perp", EntryFundingAnn: 50, QtyUSD: 1000}); err != nil {
		t.Fatal(err)
	}
	var paperID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM paper_spot_carry WHERE symbol='D468P' LIMIT 1`).Scan(&paperID); err != nil {
		t.Fatal(err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM paper_spot_carry WHERE symbol='D468P'`) // 级联删建议行
	// created_at 显式入窗：DEFAULT now()=事务时刻 ≥ 测试捕获的窗口上界 now（微秒差出窗）。
	if _, err := s.pool.Exec(ctx, `INSERT INTO carry_graduations (paper_id, venue_a, venue_b, symbol, structure, age_days, accrued_usd, created_at)
		VALUES ($1,'gate','','D468P','spot_carry',6,1,$2)`, paperID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	d, err := s.RetroDailyStats(ctx, start, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range d.OpenPos {
		if p.ID == posID {
			found = true
			if p.Venue != "gate:perp" || p.CarryUSD != 1.0313 {
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
	if d.GraduationsToday < 1 {
		t.Fatalf("graduations_today want ≥1（D-468 S4 窗口计数），got %+v", d)
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
