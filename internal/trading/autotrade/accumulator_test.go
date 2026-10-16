package autotrade

import (
	"context"
	"fmt"
	"testing"
	"time"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// testAccumStore D-080 S4 单测专用 store（复用 pgstore testStore 的 DSN 约定）。
func testAccumStore(t *testing.T) *pgstore.Store {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
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

// seedSwapIdentityMap D-558 续段：(venue, canonical) 恒等映射行落 broker_symbol_map
// （discover fixture 的 facts 符号走归一化解析须有 DB 映射行——未映射 fail-closed 拒）。
// 前置：venue 已有 broker_accounts 行（FK 父行）。
func seedSwapIdentityMap(t *testing.T, s *pgstore.Store, venue, canonical string) {
	t.Helper()
	if err := s.UpsertSymbolMapping(context.Background(),
		&pgstore.SymbolMapping{Venue: venue, BrokerSymbol: canonical, Canonical: canonical, Source: "manual"}); err != nil {
		t.Fatalf("seedSwapIdentityMap %s/%s: %v", venue, canonical, err)
	}
}

// seedDemoAccounts D-144 ① 三态：清 broker_accounts 后为指定 venues 插入 demo 行（is_real=false）。
// discoverSwapCombos 同类型配对要求两腿在 broker_accounts 有行且 is_real 非 nil；
// 旧测试（freshness/scanSwapArb 等）未插行 → 三态保守不配对 → 需此 helper 补行。
// TRUNCATE 先清表避免其他测试残留行（isreal 测试插 real/demo 混合）干扰。
func seedDemoAccounts(t *testing.T, s *pgstore.Store, venues ...string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE broker_accounts CASCADE`); err != nil {
		t.Fatalf("truncate broker_accounts: %v", err)
	}
	// D-558：本 helper=discover fixture 引导器，swap 配对 canonical（AUDJPY）顺带裁定
	// 产品类 + 逐 venue 落恒等符号映射行（归一化解析面）。
	seedTestClass(t, s, "AUDJPY", "fx")
	for _, v := range venues {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real)
			 VALUES ($1, 'mt5', 'h', 443, 'p', 's', 'connected', false)`, v); err != nil {
			t.Fatalf("seedDemoAccounts %s: %v", v, err)
		}
		seedSwapIdentityMap(t, s, v, "AUDJPY")
	}
}

// seedTestClass D-558：落产品类裁定行（discoverSwapCombos 配对闸 fail-closed——
// canonical 无裁定行即拒配对，类测试 fixture 须自带裁定）。
func seedTestClass(t *testing.T, s *pgstore.Store, canonical, class string) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(),
		`INSERT INTO fin_canonical_product_class (canonical, product_class) VALUES ($1, $2)
		 ON CONFLICT (canonical) DO NOTHING`, canonical, class); err != nil {
		t.Fatalf("seedTestClass %s: %v", canonical, err)
	}
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
	// 验证 DB：2 行（DESC 排序：day2 在前，day1 在后；D-111 排序修复）
	rows, err := s.ListComboDaily(ctx, ck, 10)
	if err != nil {
		t.Fatalf("ListComboDaily: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows (2 days), got %d", len(rows))
	}
	// day2（最新，DESC 排在首位）
	if rows[0].Samples != 1 || absFloat(rows[0].NetAvg-30) > 0.0001 {
		t.Errorf("day2: want samples=1 avg=30, got samples=%d avg=%v", rows[0].Samples, rows[0].NetAvg)
	}
	// day1（较早，DESC 排在第二）
	if rows[1].Samples != 2 || absFloat(rows[1].NetAvg-15) > 0.0001 {
		t.Errorf("day1: want samples=2 avg=15, got samples=%d avg=%v", rows[1].Samples, rows[1].NetAvg)
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

// TestAccumulator_ConcurrentNoRace R8.1 DSN 版：多 goroutine 各持独立实例并发 Record+真调 Flush。
// -race 绿（防共享单例回归）。各 goroutine 用不同 comboKey 避免 DB 行锁竞争；
// 验证无共享状态竞态 + Flush 真实落库不 panic。
func TestAccumulator_ConcurrentNoRace(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE combo_daily_rollup`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	const n = 10
	done := make(chan error, n)
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		go func(seed int) {
			acc := newComboAccumulator()
			ck := fmt.Sprintf("SWAPARB:AUDJPY:v%d:mt5:vB:mt5", seed)
			for j := 0; j < 100; j++ {
				acc.Record(ck, "SWAPARB", float64(seed+j), now)
			}
			done <- acc.Flush(ctx, s)
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatalf("goroutine %d flush: %v", i, err)
		}
	}
	// 验证 n 个组合各落库 1 行（samples=100）
	rows, err := s.Pool().Query(ctx, `SELECT combo_key, samples FROM combo_daily_rollup WHERE combo_key LIKE 'SWAPARB:AUDJPY:v%:mt5:vB:mt5'`)
	if err != nil {
		t.Fatalf("query rollup: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var ck string
		var samples int
		if err := rows.Scan(&ck, &samples); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if samples != 100 {
			t.Errorf("combo %s: want samples=100, got %d", ck, samples)
		}
		count++
	}
	if count != n {
		t.Errorf("want %d rollup rows, got %d", n, count)
	}
}

// TestD332_AccumulatorXAssetAnnualAvg T4：XASSET_CARRY netBps=年化百分点 → annual_avg=net_avg
// （与 FUNDING_HEDGE 同待遇）。红=现码白名单缺型 → annual_avg=NULL。
func TestD332_AccumulatorXAssetAnnualAvg(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE combo_daily_rollup`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	a := newComboAccumulator()
	a.Record("XASSET_CARRY:XAUUSD:277842155:mt5:binance:perp", "XASSET_CARRY", 8.1, time.Now().UTC())
	if err := a.Flush(ctx, s); err != nil {
		t.Fatalf("flush: %v", err)
	}
	rows, err := s.ListComboDaily(ctx, "XASSET_CARRY:XAUUSD:277842155:mt5:binance:perp", 10)
	if err != nil {
		t.Fatalf("ListComboDaily: %v", err)
	}
	if len(rows) != 1 || rows[0].AnnualAvg == nil || absFloat(*rows[0].AnnualAvg-8.1) > 0.0001 {
		t.Errorf("XASSET_CARRY annual_avg: want 8.1（红=现码 NULL）, got %+v", rows)
	}
}
