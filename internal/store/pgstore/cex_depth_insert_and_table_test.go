// d420_cex_depth_test.go —— D-420 S2/S4：fin_cex_depth 落库锚（DSN 门控）。
// 插入回读 + 迁移建表 + 0132 CL 映射种子在场（S3 对齐锚）。
package pgstore

import (
	"context"
	"testing"
	"time"

	"arb/migrations"
)

func TestD420_CexDepth_InsertAndTable(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE fin_cex_depth`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	rows := []CexDepthRow{{
		Venue: "gate", Symbol: "SPX500_USDT", Ts: time.Now(),
		Bid1USD: 5000.25, Ask1USD: 5000.75, Bid1Qty: 12, Ask1Qty: 8, MidDepthUSD: 5000.5 * 8,
	}}
	if err := s.InsertCexDepth(ctx, rows); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var venue, symbol string
	var bid, ask float64
	if err := s.pool.QueryRow(ctx, `SELECT venue, symbol, bid1_usd, ask1_usd FROM fin_cex_depth LIMIT 1`).
		Scan(&venue, &symbol, &bid, &ask); err != nil {
		t.Fatalf("read: %v", err)
	}
	if venue != "gate" || symbol != "SPX500_USDT" || bid != 5000.25 || ask != 5000.75 {
		t.Errorf("roundtrip: %s %s %v %v", venue, symbol, bid, ask)
	}
}

func TestD420_CLMappingSeed(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	// 同包既有测试（brokers_symbolmap_d383 等）TRUNCATE broker_symbol_map，种子行有清表
	// 顺序耦合——本测试自愈式重放 0132 种子段（幂等 ON CONFLICT），既验证真实迁移 SQL
	// 语义又不依赖执行序。
	seedBrokerVenue(t, s, "277842155", "3001157", "39050818", "59892915", "591959217") // D-496：0132 种子含 bsm 行须先有 FK 父行
	if _, err := s.pool.Exec(ctx, migrations.DataPlaneVenueSQL); err != nil {
		t.Fatalf("replay 0132: %v", err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM broker_symbol_map WHERE canonical = 'CL' AND is_primary`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 5 {
		t.Errorf("0132 CL 映射种子应 ≥5 行（油系 primary 全量），got %d", n)
	}
}
