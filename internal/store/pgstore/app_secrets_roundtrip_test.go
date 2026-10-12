// d515_secrets_test.go —— D-515 S3：app_secrets 密文通道锚测（真实 _test 隔离库）。
// 锚：解密往返 / 表非空+MASTER_KEY 缺 → fail-closed / 表空+无 KEY 不致命。
package pgstore

import (
	"testing"

	"arb/internal/platform/cryptoutil"
)

func TestD515_AppSecretsRoundtrip(t *testing.T) {
	s, ctx := d515Store(t)
	t.Setenv("ARB_MASTER_KEY", "testkey-d515")
	key, _ := cryptoutil.MasterKey()
	enc, err := cryptoutil.Seal(key, "sk-live-abc")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAppSecret(ctx, "ARB_FEISHU_APP_SECRET", enc, "seed"); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadAppSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["ARB_FEISHU_APP_SECRET"] != "sk-live-abc" {
		t.Fatal("roundtrip mismatch")
	}
}

func TestD515_AppSecretsNoKeyFatal(t *testing.T) {
	s, ctx := d515Store(t)
	if _, err := s.pool.Exec(ctx, `DELETE FROM app_secrets`); err != nil {
		t.Fatal(err)
	}
	// 表空+MASTER_KEY 缺 → 不致命。
	t.Setenv("ARB_MASTER_KEY", "")
	if got, err := s.LoadAppSecrets(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty table must not fail: err=%v", err)
	}
	// 写入一行 → 无 KEY fail-closed。
	t.Setenv("ARB_MASTER_KEY", "testkey-d515")
	key, _ := cryptoutil.MasterKey()
	enc, _ := cryptoutil.Seal(key, "v")
	if err := s.UpsertAppSecret(ctx, "ARB_AI_API_KEY", enc, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARB_MASTER_KEY", "")
	if _, err := s.LoadAppSecrets(ctx); err == nil {
		t.Fatal("non-empty app_secrets + missing key should fail closed")
	}
	// 错 key → 解密失败（不吐明文）。
	t.Setenv("ARB_MASTER_KEY", "other-key")
	if _, err := s.LoadAppSecrets(ctx); err == nil {
		t.Fatal("wrong key should fail")
	}
}
