package pgstore

// LatestXAssetEntryGate 行龄>maxAge(30min 闸值口径)→ok=false；
// 新鲜行→ok=true；noise_bps NULL（未成噪）→ok=false。

import (
	"context"
	"testing"
	"time"
)

func TestD475b_EntryGate_StaleReject(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DELETE FROM fin_xasset_monitor WHERE pair='T475B'`); err != nil {
		t.Fatalf("clean: %v", err)
	}
	ins := func(ts time.Time, noise *float64) {
		n := "NULL"
		if noise != nil {
			n = "5.0"
		}
		if _, err := s.Pool().Exec(ctx, `
			INSERT INTO fin_xasset_monitor (ts,pair,binance_bid,binance_ask,exness_bid,exness_ask,
				entry_spread_bps,exit_spread_bps,entry_net_bps,funding_rate_8h,funding_annual_pct,
				tracking_error_bps,equiv_annual_pct,noise_bps,cex_venue,mt_venue)
			VALUES ($1,'T475B',1,1,1,1,10,8,9,0.0001,10,1,10,`+n+`,'binance:perp','3001157:mt5')`, ts); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	noise := 5.0
	// 40min 前行 → maxAge=30min → ok=false。
	ins(time.Now().Add(-40*time.Minute), &noise)
	if _, _, _, _, ok, err := s.LatestXAssetEntryGate(ctx, "T475B", "binance:perp", "3001157:mt5", 30*time.Minute); err != nil || ok {
		t.Fatalf("超窗行应拒, ok=%v err=%v", ok, err)
	}
	// 10min 前行 → ok=true。
	ins(time.Now().Add(-10*time.Minute), &noise)
	if _, _, _, _, ok, err := s.LatestXAssetEntryGate(ctx, "T475B", "binance:perp", "3001157:mt5", 30*time.Minute); err != nil || !ok {
		t.Fatalf("新鲜行应放行, ok=%v err=%v", ok, err)
	}
	// noise NULL 行盖顶 → ok=false（未成噪拒用）。
	ins(time.Now(), nil)
	if _, _, _, _, ok, _ := s.LatestXAssetEntryGate(ctx, "T475B", "binance:perp", "3001157:mt5", 30*time.Minute); ok {
		t.Fatal("noise_bps NULL 应拒（未成噪）")
	}
}
