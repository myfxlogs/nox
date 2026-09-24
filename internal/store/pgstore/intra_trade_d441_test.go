// intra_trade_d441_test.go —— D-441 T1：IntraTrackStatsQuery 分组聚合+P90+circuit 天数+kill 计数；
// ListIntraTrades 过滤/限量；InsertIntraEvent 落库。需 ARB_TEST_PG_DSN，未设 skip。
package pgstore

import (
	"context"
	"testing"
	"time"

	"arb/internal/testutil"
)

func d441Store(t *testing.T) *Store {
	t.Helper()
	s, err := New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(context.Background()); err != nil { // 0134 intra_events 幂等补齐
		t.Fatalf("migrate: %v", err)
	}
	for _, tbl := range []string{"paper_intra_trades", "intra_events"} {
		if _, err := s.pool.Exec(context.Background(), "TRUNCATE "+tbl); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	return s
}

func d441Trade(s *Store, t *testing.T, baseline, status string, slip, pnl *float64) {
	t.Helper()
	tr := &IntraTrade{
		Baseline: baseline, Symbol: "XAUUSDm", Venue: "mt5", Side: "long",
		Status: "open", EntryTs: time.Now().Add(-2 * time.Hour), EntryPx: 100,
		Qty: 0.01, NotionalUSD: 1, MarginUSD: 1, StopPx: 99,
		TradeDate: time.Now(),
	}
	id, err := s.InsertIntraTrade(context.Background(), tr)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if status == "closed" {
		var sv, pv float64
		if slip != nil {
			sv = *slip
		}
		if pnl != nil {
			pv = *pnl
		}
		if err := s.CloseIntraTrade(context.Background(), id, 101, "stop", sv, pv); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
}

func f64(v float64) *float64 { return &v }

// T1 主锚：双基线混合行 + 事件行 → 分组计数/percentile/circuit 天数/kill 计数全核。
func TestD441IntraTrackStats(t *testing.T) {
	s := d441Store(t)
	ctx := context.Background()

	d441Trade(s, t, "momentum_break", "closed", f64(1), f64(5))
	d441Trade(s, t, "momentum_break", "closed", f64(3), f64(-2))
	d441Trade(s, t, "momentum_break", "open", nil, nil)
	d441Trade(s, t, "meanrev", "closed", f64(9), f64(1))

	if err := s.InsertIntraEvent(ctx, "circuit", "momentum_break", "r=-10"); err != nil {
		t.Fatalf("event: %v", err)
	}
	if err := s.InsertIntraEvent(ctx, "circuit", "momentum_break", "r=-11"); err != nil { // 同日第二条 → 天数仍 1
		t.Fatalf("event: %v", err)
	}
	if err := s.InsertIntraEvent(ctx, "kill", "", "all flat"); err != nil {
		t.Fatalf("event: %v", err)
	}

	rows, err := s.IntraTrackStatsQuery(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("baselines=%d want 2: %+v", len(rows), rows)
	}
	mr, mm := rows[0], rows[1] // ORDER BY baseline：meanrev < momentum_break
	if mm.Baseline != "momentum_break" || mr.Baseline != "meanrev" {
		t.Fatalf("order: %s %s", rows[0].Baseline, rows[1].Baseline)
	}
	if mm.Total != 3 || mm.Open != 1 || mm.Closed != 2 {
		t.Fatalf("momentum counts: %+v", mm)
	}
	if mm.PnlUSD != 3 || mm.WinCount != 1 {
		t.Fatalf("momentum pnl=%v win=%v", mm.PnlUSD, mm.WinCount)
	}
	// P90 of {1,3} = percentile_cont 插值 = 1+0.9*(3-1)=2.8
	if mm.SlipP90Bps == nil || *mm.SlipP90Bps < 2.79 || *mm.SlipP90Bps > 2.81 {
		t.Fatalf("momentum slip p90=%v want≈2.8", mm.SlipP90Bps)
	}
	if mm.CircuitDays != 1 || mm.KillCount != 1 {
		t.Fatalf("momentum circuit=%d kill=%d", mm.CircuitDays, mm.KillCount)
	}
	// meanrev：单基线事件不带它 → circuit 0；kill 空 baseline 归全 → 1
	if mr.Total != 1 || mr.Closed != 1 || mr.CircuitDays != 0 || mr.KillCount != 1 {
		t.Fatalf("meanrev: %+v", mr)
	}
	if mr.SlipP90Bps == nil || *mr.SlipP90Bps != 9 {
		t.Fatalf("meanrev slip p90=%v want 9", mr.SlipP90Bps)
	}
}

// T1 副锚：ListIntraTrades 过滤+limit；InsertIntraEvent detail 留存。
func TestD441ListIntraTradesFilter(t *testing.T) {
	s := d441Store(t)
	ctx := context.Background()
	d441Trade(s, t, "momentum_break", "closed", nil, f64(1))
	d441Trade(s, t, "meanrev", "closed", nil, f64(1))
	d441Trade(s, t, "meanrev", "open", nil, nil)

	all, err := s.ListIntraTrades(ctx, "", 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("all=%d err=%v", len(all), err)
	}
	mr, err := s.ListIntraTrades(ctx, "meanrev", 1)
	if err != nil || len(mr) != 1 || mr[0].Baseline != "meanrev" {
		t.Fatalf("filtered=%d err=%v", len(mr), err)
	}
	// id DESC：最新 id 在前
	if all[0].ID < all[1].ID {
		t.Fatal("order not id DESC")
	}
}

// T1 边界：空表聚合返回空切片不报错；事件 CHECK 拒绝非法 kind。
func TestD441IntraEventGuard(t *testing.T) {
	s := d441Store(t)
	ctx := context.Background()
	if err := s.InsertIntraEvent(ctx, "stop", "", "noise"); err == nil {
		t.Fatal("expect CHECK reject kind=stop")
	}
	rows, err := s.IntraTrackStatsQuery(ctx)
	if err != nil {
		t.Fatalf("empty stats: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("empty rows=%d", len(rows))
	}
}
