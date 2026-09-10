// D-271 S5 测试：告警评估 + 去重。
package cexscan

import (
	"context"
	"os"
	"strings"
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
		{Venue: "binance", Symbol: "BTC", Rate: 15.0}, // pct_annualized 15%/yr >= 10 → 告警
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

// TestAlertFunding_Threshold funding 告警阈值（pct_annualized 已是百分比，勿 *100）。
// rate=10.49（=10.49%/yr）>= 阈值 10 → 触发，消息含 "10.49%/yr"（非 "1049.00%/yr"）。
// rate=9.25（< 10）→ 不触发。
func TestAlertFunding_Threshold(t *testing.T) {
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

	cfg := &config.Config{CexScanFundingAprMin: 10}
	ev := NewAlertEvaluator(cfg, s)

	fundings := []FundingEntry{
		{Venue: "okx", Symbol: "BTCUSDT", Rate: 10.49},    // >= 10 → 告警
		{Venue: "binance", Symbol: "BTCUSDT", Rate: 9.25}, // < 10 → 不告警
		{Venue: "okx", Symbol: "TRXUSDT", Rate: -2.50},    // 负值 → 不告警
	}

	if err := ev.Evaluate(ctx, nil, nil, fundings); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	// 验证 alerts 表有 1 条（仅 okx/BTCUSDT 10.49 >= 10）
	var count int
	err = s.Pool().QueryRow(ctx, "SELECT count(*) FROM alerts").Scan(&count)
	if err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if count != 1 {
		t.Errorf("alerts count = %d, want 1 (only okx/BTCUSDT 10.49 >= 10)", count)
	}

	// 验证消息含 "10.49%/yr"（非 "1049.00%/yr"——P1 bug 会导致 *100）
	var msg string
	err = s.Pool().QueryRow(ctx, "SELECT message FROM alerts LIMIT 1").Scan(&msg)
	if err != nil {
		t.Fatalf("select message: %v", err)
	}
	if !strings.Contains(msg, "10.49%/yr") {
		t.Errorf("message = %q, want contains 10.49pct/yr (not *100)", msg)
	}
	if strings.Contains(msg, "1049.00%/yr") {
		t.Errorf("message = %q, contains 1049.00pct/yr (P1 bug: *100 not removed)", msg)
	}
}
