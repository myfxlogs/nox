// cex_subaccount_seeds_test.go —— D-363 S4：migration 0114 种子幂等锚。
package pgstore

import (
	"context"
	"testing"

	"arb/migrations"
)

// TestCexSubaccountSeeds_Idempotent D-363 S4：种子幂等——Migrate 后同 venue+type 各 1 行；
// 手工重跑种子 SQL（模拟重跑场景）行数不变（NOT EXISTS 保护）。
func TestCexSubaccountSeeds_Idempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE exchange_configs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 种子源：okx spot（real）+ binance spot（real）
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, simulated)
		VALUES ('okx', 'spot', 'real', 'k-okx', 's-okx', false),
		       ('binance', 'spot', 'real', 'k-bn', 's-bn', false),
		       ('gate', 'perp', 'sim', 'k-gate', 's-gate', true)`); err != nil {
		t.Fatalf("seed source rows: %v", err)
	}
	count := func(venue, vt string) int {
		var n int
		if err := s.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM exchange_configs WHERE venue=$1 AND venue_type=$2`, venue, vt).Scan(&n); err != nil {
			t.Fatalf("count %s/%s: %v", venue, vt, err)
		}
		return n
	}
	// 先执行种子 SQL（testStore 的 Migrate 已应用 0114，但 TRUNCATE 清场后需重放——
	// 同时即生产「首次应用」语义）；gate sim 不种。
	if _, err := s.pool.Exec(ctx, migrations.CexSubaccountSeedsSQL); err != nil {
		t.Fatalf("apply seeds: %v", err)
	}
	if n := count("okx", "funding"); n != 1 {
		t.Fatalf("okx/funding = %d, want 1", n)
	}
	if n := count("okx", "earn"); n != 1 {
		t.Fatalf("okx/earn = %d, want 1", n)
	}
	if n := count("binance", "funding"); n != 1 {
		t.Fatalf("binance/funding = %d, want 1", n)
	}
	if n := count("gate", "funding"); n != 0 {
		t.Fatalf("gate/funding = %d, want 0（sim 不种）", n)
	}
	// 幂等重跑：再执行种子 SQL → 行数不变（NOT EXISTS）
	if _, err := s.pool.Exec(ctx, migrations.CexSubaccountSeedsSQL); err != nil {
		t.Fatalf("re-run seeds: %v", err)
	}
	if n := count("okx", "funding") + count("okx", "earn") + count("binance", "funding"); n != 3 {
		t.Fatalf("重跑后子账户行 = %d, want 3（NOT EXISTS 幂等）", n)
	}
	// 凭据复用：okx funding 行凭据=okx spot 行（不新录密钥）
	var key string
	if err := s.pool.QueryRow(ctx,
		`SELECT api_key FROM exchange_configs WHERE venue='okx' AND venue_type='funding'`).Scan(&key); err != nil || key != "k-okx" {
		t.Errorf("okx funding api_key = %q (err=%v), want 复用 spot 行 k-okx", key, err)
	}
}

// TestD130_BinanceEarnSeed D-130 返修回归锚：0182 重放 → binance:earn 行出
// （凭据复制 spot real 行）且 binance:funding 行摘除；幂等重放行数不变。
// D-133 域1 追加：0183 counter-migration 摘除 binance:earn（业主令理财面不纳入）——
// 0182+0183 连放后 earn 行归零，0183 幂等可重放。
func TestD130_BinanceEarnSeed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE exchange_configs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, simulated)
		VALUES ('binance', 'spot', 'real', 'k-bn', 's-bn', false),
		       ('binance', 'funding', 'funding', 'k-bnf', 's-bnf', false),
		       ('binance', 'spot', 'sim', 'k-sim', 's-sim', true)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.pool.Exec(ctx, migrations.BinanceEarnSeedSQL); err != nil {
		t.Fatalf("apply 0182: %v", err)
	}
	var key string
	if err := s.pool.QueryRow(ctx,
		`SELECT api_key FROM exchange_configs WHERE venue='binance' AND venue_type='earn'`).Scan(&key); err != nil || key != "k-bn" {
		t.Fatalf("binance earn api_key=%q err=%v, want 复用 spot 行 k-bn", key, err)
	}
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM exchange_configs WHERE venue='binance' AND venue_type='funding'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("binance funding 行=%d err=%v, want 0（官方废面摘除）", n, err)
	}
	// sim spot 不种 earn（NOT simulated 同 0114 规）；幂等重放 earn 仍 1 行。
	if _, err := s.pool.Exec(ctx, migrations.BinanceEarnSeedSQL); err != nil {
		t.Fatalf("re-run 0182: %v", err)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM exchange_configs WHERE venue='binance' AND venue_type='earn'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("重放后 earn 行=%d err=%v, want 1（幂等）", n, err)
	}
	// D-133：0183 摘除 binance:earn（新环境先播种再删=净零；幂等重放仍 0 行）；
	// okx:earn 不受影响（earn 机制在域——先种一行验证不被误删）。
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, simulated)
		 VALUES ('okx', 'earn', 'earn', 'k-oe', 's-oe', false)`); err != nil {
		t.Fatalf("seed okx earn: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.pool.Exec(ctx, migrations.DropBinanceEarnSQL); err != nil {
			t.Fatalf("apply 0183 (iter %d): %v", i, err)
		}
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM exchange_configs WHERE venue='binance' AND venue_type='earn'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("0183 后 binance earn 行=%d err=%v, want 0（摘除幂等）", n, err)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM exchange_configs WHERE venue='okx' AND venue_type='earn'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("0183 后 okx earn 行=%d err=%v, want 1（不殃及在域面）", n, err)
	}
}
