// 测试：fin_cex_earn_apr + fin_cex_spread Store 方法（DSN 真实夹具）。
package pgstore

import (
	"context"
	"database/sql"
	"testing"
)

// cexScanTestStore 连接 ARB_TEST_PG_DSN，迁移后 TRUNCATE 隔离。
func cexScanTestStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	ctx := context.Background()
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_cex_earn_apr, fin_cex_spread CASCADE"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	return s
}

// TestCexScan_InsertAndLatestEarnAPR 插入 OKX+Binance 各 2 币种 → Latest 返回 4 行。
func TestCexScan_InsertAndLatestEarnAPR(t *testing.T) {
	s := cexScanTestStore(t)
	ctx := context.Background()
	rows := []*CexEarnAPR{
		{Venue: "okx", Ccy: "USDT", ProductType: "flexible", APR: 0.035},
		{Venue: "okx", Ccy: "USDC", ProductType: "flexible", APR: 0.030},
		{Venue: "binance", Ccy: "USDT", ProductType: "flexible", APR: 0.025, TierAPR: sql.NullString{String: `{"0-1000":0.025}`, Valid: true}},
		{Venue: "binance", Ccy: "USDC", ProductType: "flexible", APR: 0.020},
	}
	for _, r := range rows {
		if err := s.InsertCexEarnAPR(ctx, r); err != nil {
			t.Fatalf("InsertCexEarnAPR: %v", err)
		}
	}
	got, err := s.LatestCexEarnAPRs(ctx)
	if err != nil {
		t.Fatalf("LatestCexEarnAPRs: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d rows, want 4", len(got))
	}
	// 验证 tier_apr 透传
	var hasTier bool
	for _, r := range got {
		if r.Venue == "binance" && r.Ccy == "USDT" && r.TierAPR.Valid {
			hasTier = true
		}
	}
	if !hasTier {
		t.Error("binance USDT tier_apr not persisted")
	}
}

// TestCexScan_EarnAPRLatestOnly 插入同所同币种 2 行 → Latest 只返回最新。
func TestCexScan_EarnAPRLatestOnly(t *testing.T) {
	s := cexScanTestStore(t)
	ctx := context.Background()
	if err := s.InsertCexEarnAPR(ctx, &CexEarnAPR{Venue: "okx", Ccy: "USDT", ProductType: "flexible", APR: 0.030}); err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	if err := s.InsertCexEarnAPR(ctx, &CexEarnAPR{Venue: "okx", Ccy: "USDT", ProductType: "flexible", APR: 0.035}); err != nil {
		t.Fatalf("insert 2: %v", err)
	}
	got, err := s.LatestCexEarnAPRs(ctx)
	if err != nil {
		t.Fatalf("LatestCexEarnAPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].APR != 0.035 {
		t.Errorf("APR = %v, want 0.035 (latest)", got[0].APR)
	}
}

// TestCexScan_InsertAndLatestSpread 插入 2 symbol 价差 → Latest 返回 2 行。
func TestCexScan_InsertAndLatestSpread(t *testing.T) {
	s := cexScanTestStore(t)
	ctx := context.Background()
	rows := []*CexSpread{
		{Symbol: "BTC", VenueBuy: "binance", VenueSell: "okx", PriceBuy: 50000, PriceSell: 50050, SpreadBps: 10},
		{Symbol: "ETH", VenueBuy: "okx", VenueSell: "binance", PriceBuy: 3000, PriceSell: 3003, SpreadBps: 10},
	}
	for _, r := range rows {
		if err := s.InsertCexSpread(ctx, r); err != nil {
			t.Fatalf("InsertCexSpread: %v", err)
		}
	}
	got, err := s.LatestCexSpreads(ctx)
	if err != nil {
		t.Fatalf("LatestCexSpreads: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
}
