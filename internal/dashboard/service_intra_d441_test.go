// service_intra_d441_test.go —— D-441 T4：IntraTrackStats 空表 days_elapsed=0 无除零；
// ListIntraTrades limit 夹取+baseline 过滤。需 ARB_TEST_PG_DSN。
package dashboard

import (
	"context"
	"testing"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"

	"connectrpc.com/connect"
)

func intraTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	if _, err := st.Pool().Exec(context.Background(),
		`TRUNCATE paper_intra_trades, intra_events RESTART IDENTITY`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	return st
}

// T4 主锚：空表 → days_elapsed=0、days_to_gate=30、无除零 panic。
func TestD441IntraTrackStatsEmpty(t *testing.T) {
	st := intraTestStore(t)
	svc := &Service{store: st}
	resp, err := svc.IntraTrackStats(context.Background(),
		connect.NewRequest(&v1.IntraTrackStatsRequest{}))
	if err != nil {
		t.Fatalf("IntraTrackStats: %v", err)
	}
	if resp.Msg.DaysElapsed != 0 || resp.Msg.DaysToGate != 30 {
		t.Fatalf("elapsed=%d toGate=%d want 0/30", resp.Msg.DaysElapsed, resp.Msg.DaysToGate)
	}
	if len(resp.Msg.Baselines) != 0 {
		t.Fatalf("baselines=%d want 0", len(resp.Msg.Baselines))
	}
}

// T4 副锚：有成交 → days_elapsed≥1、limit 夹取 [1,1000]、baseline 过滤。
func TestD441IntraTrackStatsAndList(t *testing.T) {
	st := intraTestStore(t)
	ctx := context.Background()
	svc := &Service{store: st}
	tr := &pgstore.IntraTrade{
		Baseline: "momentum_break", Symbol: "XAUUSDm", Venue: "mt5", Side: "long",
		Status: "open", EntryTs: time.Now().Add(-48 * time.Hour), EntryPx: 100,
		Qty: 0.01, NotionalUSD: 1, MarginUSD: 1, StopPx: 99,
		TradeDate: time.Now(),
	}
	id, err := st.InsertIntraTrade(ctx, tr)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := st.CloseIntraTrade(ctx, id, 101, "stop", 1.5, 2.0); err != nil {
		t.Fatalf("close: %v", err)
	}

	stats, err := svc.IntraTrackStats(ctx, connect.NewRequest(&v1.IntraTrackStatsRequest{}))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Msg.DaysElapsed < 1 || stats.Msg.DaysToGate >= 30 {
		t.Fatalf("elapsed=%d toGate=%d", stats.Msg.DaysElapsed, stats.Msg.DaysToGate)
	}
	if len(stats.Msg.Baselines) != 1 || stats.Msg.Baselines[0].Total != 1 || stats.Msg.Baselines[0].PnlUsd != 2 {
		t.Fatalf("baselines=%v", stats.Msg.Baselines)
	}

	list, err := svc.ListIntraTrades(ctx, connect.NewRequest(&v1.ListIntraTradesRequest{Limit: 5000}))
	if err != nil || len(list.Msg.Trades) != 1 {
		t.Fatalf("list=%d err=%v", len(list.Msg.Trades), err)
	}
	row := list.Msg.Trades[0]
	if row.Baseline != "momentum_break" || row.ExitTs == 0 || row.PnlUsd == nil || *row.PnlUsd != 2 {
		t.Fatalf("row=%+v", row)
	}
	// baseline 过滤 miss → 空
	list2, err := svc.ListIntraTrades(ctx, connect.NewRequest(&v1.ListIntraTradesRequest{Baseline: "meanrev"}))
	if err != nil || len(list2.Msg.Trades) != 0 {
		t.Fatalf("filtered=%d err=%v", len(list2.Msg.Trades), err)
	}
}
