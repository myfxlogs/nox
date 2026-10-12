// 测试：TableMaxTs 白名单命中/未命中/error 三态（DSN 真实夹具）。
package pgstore

import (
	"context"
	"testing"
	"time"
)

// tableHealthTestStore 连接 ARB_TEST_PG_DSN，迁移后隔离。
func tableHealthTestStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	return s
}

// TestTableMaxTs_WhitelistHit 白名单命中 → 执行 SQL 返回时间戳。
func TestTableMaxTs_WhitelistHit(t *testing.T) {
	s := tableHealthTestStore(t)
	ctx := context.Background()

	// D-316①：TRUNCATE 自隔离——全量包跑时先行测试经 pgstore API 插入的 now 行会污染
	// max(ts) 精确断言（master 全量同红实证、-run 子集绿=D-301a 验收未暴露）。
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_cex_spread"); err != nil {
		t.Fatalf("TRUNCATE fin_cex_spread: %v", err)
	}
	// 插入一行 fin_cex_spread → max(ts) 应返回该时间戳。
	ts := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO fin_cex_spread (symbol, venue_buy, venue_sell, price_buy, price_sell, spread_bps, ts)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		"BTCUSDT", "binance", "okx", 50000.0, 50010.0, 2.0, ts); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.TableMaxTs(ctx, "cex_spread")
	if err != nil {
		t.Fatalf("TableMaxTs cex_spread: %v", err)
	}
	if got.IsZero() {
		t.Fatal("TableMaxTs cex_spread returned zero time after insert")
	}
	// 比较秒级（pg TIMESTAMPTZ 微秒精度与 Go time 可能微秒级差异）
	if got.Unix() != ts.Unix() {
		t.Errorf("TableMaxTs cex_spread want ts~%v, got %v", ts, got)
	}
}

// TestTableMaxTs_EmptyTable 空表 → 零值时间（调用方视为 crit）。
func TestTableMaxTs_EmptyTable(t *testing.T) {
	s := tableHealthTestStore(t)
	ctx := context.Background()

	// 清空 fin_cex_spread（prepareTestSchema 已建表，TRUNCATE 隔离）
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_cex_spread"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	got, err := s.TableMaxTs(ctx, "cex_spread")
	if err != nil {
		t.Fatalf("TableMaxTs empty table should not error, got %v", err)
	}
	if !got.IsZero() {
		t.Errorf("TableMaxTs empty table want zero time, got %v", got)
	}
}

// TestTableMaxTs_UnknownName 白名单未命中 → error（防绕过白名单注入外部表名）。
func TestTableMaxTs_UnknownName(t *testing.T) {
	s := tableHealthTestStore(t)
	ctx := context.Background()

	_, err := s.TableMaxTs(ctx, "evil_table; DROP TABLE users; --")
	if err == nil {
		t.Fatal("TableMaxTs unknown name should error")
	}
}

// TestTableMaxTs_FactsQuery funding_hist_binance 查询走 facts 表 WHERE 子句。
func TestTableMaxTs_FactsQuery(t *testing.T) {
	s := tableHealthTestStore(t)
	ctx := context.Background()

	// 清空 facts 隔离
	if _, err := s.pool.Exec(ctx, "TRUNCATE facts"); err != nil {
		t.Fatalf("TRUNCATE facts: %v", err)
	}
	// 空表 → 零值
	got, err := s.TableMaxTs(ctx, "funding_hist_binance")
	if err != nil {
		t.Fatalf("TableMaxTs funding_hist_binance empty: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("empty facts want zero time, got %v", got)
	}
}

// TestTableMaxTs_DailyIncomeCoversToPeriodEnd C1（D-315）：day 是 DATE 日粒度——day=昨日 的行
// 覆盖到 今日 00:00，TableMaxTs 应返回覆盖期末（age<24h 不告警），非行标签起点（旧码返回
// 昨日 00:00，健康日 age 落 24-48h 假 crit）。
func TestTableMaxTs_DailyIncomeCoversToPeriodEnd(t *testing.T) {
	s := tableHealthTestStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE position_daily_income"); err != nil {
		t.Fatalf("TRUNCATE position_daily_income: %v", err)
	}
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO position_daily_income (day) VALUES ($1)`, yesterday); err != nil {
		t.Fatalf("insert day=yesterday: %v", err)
	}
	got, err := s.TableMaxTs(ctx, "daily_income")
	if err != nil {
		t.Fatalf("TableMaxTs daily_income: %v", err)
	}
	want := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, 1) // 覆盖期末 = day+1 的 00:00 UTC
	if got.Unix() != want.Unix() {
		t.Errorf("C1: daily_income day=yesterday want coverage-end %v, got %v", want, got)
	}
	if age := time.Since(got); age >= 24*time.Hour {
		t.Errorf("C1: healthy rollup age want <24h, got %v (would false-crit)", age)
	}
}

// TestTableMaxTs_DailyIncomeMissingDayCrossesThreshold C2（D-315）：day=前日 → 覆盖期末=
// 昨日 00:00 → age≥24h 越线可告警（漏写一日起 warn/crit 继续爬升，监控意图不变）。
func TestTableMaxTs_DailyIncomeMissingDayCrossesThreshold(t *testing.T) {
	s := tableHealthTestStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE position_daily_income"); err != nil {
		t.Fatalf("TRUNCATE position_daily_income: %v", err)
	}
	dayBefore := time.Now().UTC().AddDate(0, 0, -2)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO position_daily_income (day) VALUES ($1)`, dayBefore); err != nil {
		t.Fatalf("insert day=day-before-yesterday: %v", err)
	}
	got, err := s.TableMaxTs(ctx, "daily_income")
	if err != nil {
		t.Fatalf("TableMaxTs daily_income: %v", err)
	}
	want := time.Date(dayBefore.Year(), dayBefore.Month(), dayBefore.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, 1) // 覆盖期末 = 昨日 00:00 UTC
	if got.Unix() != want.Unix() {
		t.Errorf("C2: daily_income day=day-before want coverage-end %v, got %v", want, got)
	}
	if age := time.Since(got); age < 24*time.Hour {
		t.Errorf("C2: missing-day age want >=24h (alertable), got %v", age)
	}
}
