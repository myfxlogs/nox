// venue_io_test.go —— D-365 S5：fin_venue_io 幂等+水位+Recent 锚。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func TestVenueIO(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE fin_venue_io`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ts := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	fee := 1.5
	rows := []VenueIO{
		{Venue: "binance", ConfigID: 20, Direction: "deposit", Ccy: "USDT", Amount: 22000,
			Status: "1", TxID: "0xabc", ExternalID: "5232145709440380673", Ts: ts},
		{Venue: "binance", ConfigID: 20, Direction: "withdraw", Ccy: "TRX", Amount: 379.0191,
			Fee: &fee, Status: "6", TxID: "0xdef", ExternalID: "999", Ts: ts.Add(time.Hour)},
		{Venue: "okx", ConfigID: 30, Direction: "deposit", Ccy: "USDT", Amount: 400,
			Status: "2", ExternalID: "431879636", Ts: ts.Add(2 * time.Hour)},
	}
	n, err := s.InsertVenueIO(ctx, rows)
	if err != nil || n != 3 {
		t.Fatalf("insert = %d (err=%v), want 3", n, err)
	}
	// 幂等：同行重插 → 0 新增
	n, err = s.InsertVenueIO(ctx, rows)
	if err != nil || n != 0 {
		t.Fatalf("re-insert = %d (err=%v), want 0（幂等）", n, err)
	}
	// 水位游标：deposit 方向 = ts（第一行）
	wm, ok, err := s.LatestVenueIOTs(ctx, 20, "deposit")
	if err != nil || !ok || !wm.Equal(ts) {
		t.Fatalf("watermark deposit = %v ok=%v err=%v, want %v", wm, ok, err, ts)
	}
	wm, ok, _ = s.LatestVenueIOTs(ctx, 20, "withdraw")
	if !ok || !wm.Equal(ts.Add(time.Hour)) {
		t.Fatalf("watermark withdraw = %v, want +1h", wm)
	}
	if _, ok, _ := s.LatestVenueIOTs(ctx, 99, "deposit"); ok {
		t.Error("空配置水位应 ok=false")
	}
	// Recent：config 20 两行 ts 倒序
	rec, err := s.RecentVenueIO(ctx, "binance", 20, 10)
	if err != nil || len(rec) != 2 {
		t.Fatalf("recent = %d (err=%v), want 2", len(rec), err)
	}
	if rec[0].Direction != "withdraw" || rec[1].Direction != "deposit" {
		t.Errorf("recent 顺序应 ts 倒序: %s then %s", rec[0].Direction, rec[1].Direction)
	}
	if rec[0].Fee == nil || *rec[0].Fee != 1.5 {
		t.Errorf("withdraw fee want 1.5, got %v", rec[0].Fee)
	}
}
