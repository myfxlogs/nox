// D-266 S1 测试：fin_repo_rates Store 方法（DSN 真实夹具）。
package pgstore

import (
	"context"
	"testing"
	"time"

	"arb/internal/testutil"
)

func ptrFloat(v float64) *float64 { return &v }

// repoTestStore 连接 ARB_TEST_PG_DSN 测试库，TRUNCATE fin_repo_rates + alerts 隔离数据。
func repoTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_repo_rates, alerts CASCADE"); err != nil {
		t.Fatalf("TRUNCATE fin_repo_rates, alerts: %v", err)
	}
	return s
}

// TestRepoRate_InsertAndLatest 插入 3 行 GC007（3 个时间点）→ LatestRepoRates 返回最新 1 行。
func TestRepoRate_InsertAndLatest(t *testing.T) {
	s := repoTestStore(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	// 3 行 GC007，时间递增，rate 递增
	for i, rate := range []float64{2.0, 2.5, 3.0} {
		ts := day.Add(time.Duration(i) * time.Hour)
		if err := s.InsertRepoRate(ctx, "204007", "GC007", rate, nil, day); err != nil {
			t.Fatalf("InsertRepoRate %d: %v", i, err)
		}
		_ = ts // ts 由 DB 默认 now() 生成；trade_date 显式传
	}
	// 短暂 sleep 确保 ts 递增（DB now() 精度到微秒，快速插入可能同 ts）
	time.Sleep(10 * time.Millisecond)
	// 再插 1 行确保是最新
	if err := s.InsertRepoRate(ctx, "204007", "GC007", 3.5, nil, day); err != nil {
		t.Fatalf("InsertRepoRate latest: %v", err)
	}
	rates, err := s.LatestRepoRates(ctx)
	if err != nil {
		t.Fatalf("LatestRepoRates: %v", err)
	}
	// 只插了 GC007，应返回 1 行
	if len(rates) != 1 {
		t.Fatalf("LatestRepoRates len = %d, want 1", len(rates))
	}
	if rates[0].Code != "204007" {
		t.Errorf("Code = %q, want 204007", rates[0].Code)
	}
	if rates[0].RatePct != 3.5 {
		t.Errorf("RatePct = %v, want 3.5 (latest)", rates[0].RatePct)
	}
}

// TestRepoRate_30dAvg 30 天均值：插入 3 行 → 均值正确。
func TestRepoRate_30dAvg(t *testing.T) {
	s := repoTestStore(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	for _, rate := range []float64{2.0, 3.0, 4.0} {
		if err := s.InsertRepoRate(ctx, "204007", "GC007", rate, nil, day); err != nil {
			t.Fatalf("InsertRepoRate: %v", err)
		}
	}
	avg, has, err := s.RepoRate30dAvg(ctx, "204007")
	if err != nil {
		t.Fatalf("RepoRate30dAvg: %v", err)
	}
	if !has {
		t.Fatal("has = false, want true (有数据)")
	}
	// 均值 = (2.0+3.0+4.0)/3 = 3.0
	if avg < 2.9 || avg > 3.1 {
		t.Errorf("30d avg = %.4f, want ~3.0", avg)
	}
}

// TestRepoRate_DailyOHLC_WithData 有数据日 → 四值正确。
func TestRepoRate_DailyOHLC_WithData(t *testing.T) {
	s := repoTestStore(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	// 插入 4 行：open=2.0, high=4.0, low=1.5, close=3.0（按 ts 升序）
	for i, rate := range []float64{2.0, 1.5, 4.0, 3.0} {
		ts := day.Add(time.Duration(i) * time.Hour)
		if err := s.InsertRepoRate(ctx, "204007", "GC007", rate, nil, day); err != nil {
			t.Fatalf("InsertRepoRate %d: %v", i, err)
		}
		_ = ts
	}
	// 短暂 sleep 后再插确保 close 是最后插入的
	time.Sleep(10 * time.Millisecond)
	if err := s.InsertRepoRate(ctx, "204007", "GC007", 3.0, nil, day); err != nil {
		t.Fatalf("InsertRepoRate close: %v", err)
	}
	open, close_, high, low, err := s.RepoRateDailyOHLC(ctx, "204007", day)
	if err != nil {
		t.Fatalf("RepoRateDailyOHLC: %v", err)
	}
	if open != 2.0 {
		t.Errorf("open = %v, want 2.0", open)
	}
	if low != 1.5 {
		t.Errorf("low = %v, want 1.5", low)
	}
	if high != 4.0 {
		t.Errorf("high = %v, want 4.0", high)
	}
	if close_ != 3.0 {
		t.Errorf("close = %v, want 3.0", close_)
	}
}

// TestRepoRate_DailyOHLC_NoData 空表/无数据日 → 全零不报错（COALESCE 兜底）。
func TestRepoRate_DailyOHLC_NoData(t *testing.T) {
	s := repoTestStore(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	open, close_, high, low, err := s.RepoRateDailyOHLC(ctx, "204007", day)
	if err != nil {
		t.Fatalf("RepoRateDailyOHLC empty: %v", err)
	}
	if open != 0 || close_ != 0 || high != 0 || low != 0 {
		t.Errorf("empty day OHLC should be 0, got open=%v close=%v high=%v low=%v", open, close_, high, low)
	}
}

// TestAlreadyAlertedToday 插入 1 条 alert 后返回 true；未插入 pattern 返回 false。
func TestAlreadyAlertedToday(t *testing.T) {
	s := repoTestStore(t)
	ctx := context.Background()
	// 未插入 → false
	alerted, err := s.AlreadyAlertedToday(ctx, "[repo] GC007%")
	if err != nil {
		t.Fatalf("AlreadyAlertedToday (empty): %v", err)
	}
	if alerted {
		t.Error("empty alerts: AlreadyAlertedToday = true, want false")
	}
	// 插入 1 条匹配 alert
	if err := s.InsertAlert(ctx, nil, "warn", "[repo] GC007 年化 3.5%，建议逆回购"); err != nil {
		t.Fatalf("InsertAlert: %v", err)
	}
	// 匹配 pattern → true
	alerted, err = s.AlreadyAlertedToday(ctx, "[repo] GC007%")
	if err != nil {
		t.Fatalf("AlreadyAlertedToday (after insert): %v", err)
	}
	if !alerted {
		t.Error("after insert: AlreadyAlertedToday = false, want true")
	}
	// 不匹配 pattern → false（每品种独立去重，审计 A1）
	alerted, err = s.AlreadyAlertedToday(ctx, "[repo] GC001%")
	if err != nil {
		t.Fatalf("AlreadyAlertedToday (GC001): %v", err)
	}
	if alerted {
		t.Error("GC001 pattern: AlreadyAlertedToday = true, want false (每品种独立去重)")
	}
}

// TestRepoRate_TradeDateShanghaiTZ 上海时区 tradeDate 不偏移到前一天。
// R1 回归：time.Date(2026,9,10,0,0,0,0,CST) 传 DATE 列应存 2026-09-10 非 2026-09-09。
func TestRepoRate_TradeDateShanghaiTZ(t *testing.T) {
	s := repoTestStore(t)
	ctx := context.Background()
	loc := time.FixedZone("CST", 8*3600)
	shanghaiMidnight := time.Date(2026, 9, 10, 0, 0, 0, 0, loc)
	if err := s.InsertRepoRate(ctx, "204007", "GC007", 1.5, nil, shanghaiMidnight); err != nil {
		t.Fatalf("InsertRepoRate: %v", err)
	}
	var tradeDate string
	if err := s.pool.QueryRow(ctx,
		`SELECT trade_date::text FROM fin_repo_rates WHERE code='204007' LIMIT 1`).Scan(&tradeDate); err != nil {
		t.Fatalf("query trade_date: %v", err)
	}
	if tradeDate != "2026-09-10" {
		t.Errorf("trade_date = %s, want 2026-09-10（上海午夜不应偏移到前一天）", tradeDate)
	}
}
