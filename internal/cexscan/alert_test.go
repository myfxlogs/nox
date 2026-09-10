// D-271 S5 测试：告警评估 + 去重。
package cexscan

import (
	"context"
	"os"
	"testing"
	"time"

	"arb/internal/config"
	"arb/internal/store/pgstore"
)

// TestAlert_Dedup5MinTTL 5 分钟内同 key 去重。
func TestAlert_Dedup5MinTTL(t *testing.T) {
	ev := NewAlertEvaluator(&config.Config{}, nil)
	now := time.Now()
	if !ev.shouldAlert("earn:okx:USDT", now) {
		t.Error("first call should alert")
	}
	if ev.shouldAlert("earn:okx:USDT", now.Add(1*time.Minute)) {
		t.Error("should be deduped within 5 min")
	}
	if !ev.shouldAlert("earn:okx:USDT", now.Add(6*time.Minute)) {
		t.Error("should alert after 5 min TTL")
	}
}

// TestAlert_DedupDifferentKeys 不同 key 不去重。
func TestAlert_DedupDifferentKeys(t *testing.T) {
	ev := NewAlertEvaluator(&config.Config{}, nil)
	now := time.Now()
	if !ev.shouldAlert("earn:okx:USDT", now) {
		t.Error("first call should alert")
	}
	if !ev.shouldAlert("earn:binance:USDT", now) {
		t.Error("different key should alert")
	}
	if !ev.shouldAlert("spread:BTC", now) {
		t.Error("different rule should alert")
	}
}

// TestAlert_EvaluateDSN DSN 集成测试：验证 Evaluate 调用 InsertAlert + 去重。
func TestAlert_EvaluateDSN(t *testing.T) {
	dsn := os.Getenv("ARB_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("ARB_TEST_PG_DSN not set")
	}
	ctx := context.Background()
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, "TRUNCATE alerts CASCADE"); err != nil {
		t.Fatalf("TRUNCATE alerts: %v", err)
	}

	cfg := &config.Config{CexScanEarnAprMin: 0.03, CexScanSpreadBps: 50, CexScanFundingAprMin: 10}
	ev := NewAlertEvaluator(cfg, s)

	earnAPRs := []pgstore.CexEarnAPR{
		{Venue: "okx", Ccy: "USDT", APR: 0.035}, // >= 0.03 → 告警
		{Venue: "okx", Ccy: "USDC", APR: 0.02},  // < 0.03 → 不告警
	}
	spreads := []SpreadResult{
		{Symbol: "BTC", VenueBuy: "okx", VenueSell: "binance", SpreadBps: 60}, // >= 50 → 告警
		{Symbol: "ETH", VenueBuy: "okx", VenueSell: "binance", SpreadBps: 30}, // < 50 → 不告警
	}
	fundings := []FundingEntry{
		{Venue: "binance", Symbol: "BTC", Rate: 0.15}, // 15% >= 10 → 告警
	}

	if err := ev.Evaluate(ctx, earnAPRs, spreads, fundings); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	var count int
	err = s.Pool().QueryRow(ctx, "SELECT count(*) FROM alerts").Scan(&count)
	if err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if count != 3 {
		t.Errorf("alerts count = %d, want 3", count)
	}

	// 去重：再调一次，不产生新告警
	if err := ev.Evaluate(ctx, earnAPRs, spreads, fundings); err != nil {
		t.Fatalf("Evaluate 2nd: %v", err)
	}
	err = s.Pool().QueryRow(ctx, "SELECT count(*) FROM alerts").Scan(&count)
	if err != nil {
		t.Fatalf("count alerts 2nd: %v", err)
	}
	if count != 3 {
		t.Errorf("alerts count after dedup = %d, want 3 (deduped)", count)
	}
}
