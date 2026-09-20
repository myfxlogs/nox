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
