// d515_seal_test.go —— D-515 A3/A4：boot 自封幂等+MASTER_KEY 缺位 fail-closed。
// 需 ARB_TEST_PG_DSN（_test 库）。ARB_MASTER_KEY 由测试注入（testkey，禁区不越——生成归业主）。
package pgstore

import (
	"context"
	"testing"

	"arb/internal/platform/testutil"
)

func d515Store(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	s, err := New(ctx, testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore new: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM exchange_configs`); err != nil {
		t.Fatalf("clean: %v", err)
	}
	return s, ctx
}

func TestD515_BootSealIdempotent(t *testing.T) {
	s, ctx := d515Store(t)
	t.Setenv("ARB_MASTER_KEY", "testkey-d515")
	id, err := insertPlainRow(ctx, s)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 一封：明文→enc+清空。
	n, err := s.SealExchangeKeys(ctx)
	if err != nil || n != 1 {
		t.Fatalf("seal n=%d err=%v", n, err)
	}
	var plain string
	var encNull bool
	if err := s.pool.QueryRow(ctx,
		`SELECT api_key, api_key_enc IS NULL FROM exchange_configs WHERE id=$1`, id).Scan(&plain, &encNull); err != nil {
		t.Fatal(err)
	}
	if plain != "" || encNull {
		t.Fatalf("post-seal: plain=%q encNull=%v", plain, encNull)
	}
	// 二跑幂等零变更。
	n2, err := s.SealExchangeKeys(ctx)
	if err != nil || n2 != 0 {
		t.Fatalf("re-seal n=%d err=%v", n2, err)
	}
	// 读面解密回填。
	cfgs, err := s.ListExchangeConfigs(ctx)
	if err != nil || len(cfgs) != 1 || cfgs[0].APIKey != "k-plain" || cfgs[0].APISecret != "s-plain" {
		t.Fatalf("decrypt read: %+v err=%v", cfgs, err)
	}
}

func TestD515_MasterKeyMissingFatal(t *testing.T) {
	s, ctx := d515Store(t)
	// 无 MASTER_KEY + 明文行 → SealExchangeKeys 错（boot fatal 路径）。
	t.Setenv("ARB_MASTER_KEY", "")
	if _, err := insertPlainRow(ctx, s); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.SealExchangeKeys(ctx); err == nil {
		t.Fatal("missing master key + pending rows should fail")
	}
	// enc 行存在 + 无 key → ListExchangeConfigs 错（读面 fail-closed）。
	t.Setenv("ARB_MASTER_KEY", "testkey-d515")
	if _, err := s.SealExchangeKeys(ctx); err != nil {
		t.Fatalf("seal: %v", err)
	}
	t.Setenv("ARB_MASTER_KEY", "")
	if _, err := s.ListExchangeConfigs(ctx); err == nil {
		t.Fatal("enc rows + missing key should fail closed")
	}
	// 错 key 解密失败。
	t.Setenv("ARB_MASTER_KEY", "other-key")
	if _, err := s.ListExchangeConfigs(ctx); err == nil {
		t.Fatal("wrong key should fail")
	}
}

func insertPlainRow(ctx context.Context, s *Store) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated)
		VALUES ('binance','spot','t','k-plain','s-plain','p-plain','',false) RETURNING id`).Scan(&id)
	return id, err
}
