// perf_baseline_test.go —— D-535 业绩基线锚读 + 净流扣减锚测。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func TestPerfBaseline_AnchorAndFlow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// 锚键播种（迁移 1030 同键——测试内显式种，不依赖共享 schema 种子态）。
	for _, kv := range [][2]string{
		{PerfBaselineUSDKey, "11317"},
		{PerfBaselineAtKey, "2026-10-15T00:00:00+08:00"},
		{PerfBaselineCcyKey, "USDT+USD"},
		{PerfFlowProvisional, "false"},
	} {
		if err := s.InsertAppConfigIfAbsent(ctx, kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.PerfBaseline(ctx)
	if err != nil || !b.OK {
		t.Fatalf("baseline: %+v err=%v", b, err)
	}
	if b.BaselineUSD != 11317 || b.CcyScope != "USDT+USD" {
		t.Fatalf("baseline=%+v", b)
	}
	// 净流：充 100 USDT − 提 20 USDT + BTC 充币披露（不入本金流）+ transfer 划转不计。
	base := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	rows := []VenueIO{
		{Venue: "binance", ConfigID: 1, Direction: "deposit", Ccy: "USDT", Amount: 100, ExternalID: "d1", Ts: base.Add(time.Hour)},
		{Venue: "binance", ConfigID: 1, Direction: "withdraw", Ccy: "USDT", Amount: 20, ExternalID: "w1", Ts: base.Add(2 * time.Hour)},
		{Venue: "okx", ConfigID: 2, Direction: "deposit", Ccy: "BTC", Amount: 0.5, ExternalID: "d2", Ts: base.Add(time.Hour)},
		{Venue: "binance", ConfigID: 1, Direction: "transfer", Ccy: "USDT", Amount: 500, ExternalID: "t1", Ts: base.Add(time.Hour)},
		{Venue: "binance", ConfigID: 1, Direction: "deposit", Ccy: "USDT", Amount: 999, ExternalID: "old", Ts: base.Add(-time.Hour)}, // 基线前不计
	}
	if _, err := s.InsertVenueIO(ctx, rows); err != nil {
		t.Fatal(err)
	}
	net, skipped, err := s.NetExternalFlowSince(ctx, base.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if net != 80 || skipped != 1 {
		t.Fatalf("net=%v skipped=%d, want 80/1", net, skipped)
	}
}
