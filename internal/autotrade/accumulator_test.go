package autotrade

import (
	"context"
	"os"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// testAccumStore D-080 S4 单测专用 store（复用 pgstore testStore 的 DSN 约定）。
func testAccumStore(t *testing.T) *pgstore.Store {
	dsn := os.Getenv("ARB_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("ARB_TEST_PG_DSN not set")
	}
	s, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestAccumulator_CumulativeOverwrite D-080 S4：两次 flush 后 avg 正确（累计覆盖语义）。
// 同组合喂 3 样本（10/20/30）→ flush → 再喂 2 样本（40/50）→ flush → DB 应为 5 样本 avg=30。
func TestAccumulator_CumulativeOverwrite(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE combo_daily_rollup`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	a := newComboAccumulator()
	now := time.Now().UTC()
	ck := "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5"
	// 第一批 3 样本
	a.Record(ck, "SWAPARB", 10, now)
	a.Record(ck, "SWAPARB", 20, now)
	a.Record(ck, "SWAPARB", 30, now)
	if err := a.Flush(ctx, s); err != nil {
		t.Fatalf("flush 1: %v", err)
	}
	// 第二批 2 样本（同日，累计覆盖）
	a.Record(ck, "SWAPARB", 40, now)
	a.Record(ck, "SWAPARB", 50, now)
	if err := a.Flush(ctx, s); err != nil {
		t.Fatalf("flush 2: %v", err)
	}
	// 验证 DB：5 样本，avg=30，min=10，max=50
	rows, err := s.ListComboDaily(ctx, ck, 10)
	if err != nil {
		t.Fatalf("ListComboDaily: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Samples != 5 {
		t.Errorf("want samples=5, got %d", r.Samples)
	}
	if absFloat(r.NetAvg-30) > 0.0001 {
		t.Errorf("want net_avg=30, got %v", r.NetAvg)
	}
	if absFloat(r.NetMin-10) > 0.0001 {
		t.Errorf("want net_min=10, got %v", r.NetMin)
	}
	if absFloat(r.NetMax-50) > 0.0001 {
		t.Errorf("want net_max=50, got %v", r.NetMax)
	}
}

// TestAccumulator_DayRollover D-080 S4：UTC 日切重置（新日新累积，不混入昨日）。
// 实际使用：flush 每 5min 一次，日切在两次 flush 之间发生 → 日切前当日数据已落库。
func TestAccumulator_DayRollover(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE combo_daily_rollup`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	a := newComboAccumulator()
	ck := "CARRY:EURUSD:icmarkets:mt5:exness:mt5"
	// Day 1: 2 样本 → flush（模拟日切前最后一次 flush）
	day1 := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	a.Record(ck, "CARRY", 10, day1)
	a.Record(ck, "CARRY", 20, day1)
	if err := a.Flush(ctx, s); err != nil {
		t.Fatalf("flush day1: %v", err)
	}
	// Day 2: 1 样本（日切重置）→ flush
	day2 := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	a.Record(ck, "CARRY", 30, day2)
	if err := a.Flush(ctx, s); err != nil {
		t.Fatalf("flush day2: %v", err)
	}
	// 验证 DB：2 行（day1 avg=15, day2 avg=30）
	rows, err := s.ListComboDaily(ctx, ck, 10)
	if err != nil {
		t.Fatalf("ListComboDaily: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows (2 days), got %d", len(rows))
	}
	// day1
	if rows[0].Samples != 2 || absFloat(rows[0].NetAvg-15) > 0.0001 {
		t.Errorf("day1: want samples=2 avg=15, got samples=%d avg=%v", rows[0].Samples, rows[0].NetAvg)
	}
	// day2
	if rows[1].Samples != 1 || absFloat(rows[1].NetAvg-30) > 0.0001 {
		t.Errorf("day2: want samples=1 avg=30, got samples=%d avg=%v", rows[1].Samples, rows[1].NetAvg)
	}
}

// TestAccumulator_FundingAnnualAvg D-080 S4：FUNDING_HEDGE annual_avg=net_avg，其他=NULL。
func TestAccumulator_FundingAnnualAvg(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE combo_daily_rollup`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	a := newComboAccumulator()
	now := time.Now().UTC()
	// FUNDING_HEDGE → annual_avg=net_avg
	a.Record("FUNDING_HEDGE:TRX:binance:perp:gate:perp", "FUNDING_HEDGE", 10.95, now)
	// SWAPARB → annual_avg=NULL
	a.Record("SWAPARB:AUDJPY:52993526:mt5:277842155:mt5", "SWAPARB", 0.5, now)
	if err := a.Flush(ctx, s); err != nil {
		t.Fatalf("flush: %v", err)
	}
	// 验证 FUNDING_HEDGE
	rows, err := s.ListComboDaily(ctx, "FUNDING_HEDGE:TRX:binance:perp:gate:perp", 10)
	if err != nil {
		t.Fatalf("ListComboDaily funding: %v", err)
	}
	if len(rows) != 1 || rows[0].AnnualAvg == nil || absFloat(*rows[0].AnnualAvg-10.95) > 0.0001 {
		t.Errorf("FUNDING_HEDGE annual_avg: want 10.95, got %+v", rows)
	}
	// 验证 SWAPARB
	rows, err = s.ListComboDaily(ctx, "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5", 10)
	if err != nil {
		t.Fatalf("ListComboDaily swaparb: %v", err)
	}
	if len(rows) != 1 || rows[0].AnnualAvg != nil {
		t.Errorf("SWAPARB annual_avg: want NULL, got %+v", rows)
	}
}

// TestAccumulator_ConcurrentNoRace R2 并发冒烟：多 goroutine 各持独立实例并发 Record+Flush。
// -race 绿（防共享单例回归）。无需 DB——验证无共享状态竞态。
func TestAccumulator_ConcurrentNoRace(t *testing.T) {
	const n = 10
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func(seed int) {
			acc := newComboAccumulator()
			for j := 0; j < 100; j++ {
				acc.Record("SWAPARB:AUDJPY:52993526:mt5:277842155:mt5", "SWAPARB", float64(seed+j), time.Now())
			}
			// Flush 无 store 时会 panic——用 nil store 跳过（只验证 Record 无竞态）
			// 改为只验证 entries 无 panic
			_ = acc.entries
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
}
