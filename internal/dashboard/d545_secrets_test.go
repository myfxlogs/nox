package dashboard

// D-545 验收锚：T2=UpdateAppSecret 写后 DB 密文非明文+resolver 立即新值+审计 key-only
// +note 必填拒+非白名单拒；T3=空值清行→行删+resolver.Unset+List has_value 翻 false。

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"arb/internal/config"
	"arb/internal/cryptoutil"
	dashv1 "arb/internal/gen/dashboardv1"
	"arb/internal/testutil/storetest"
)

func d545Svc(t *testing.T) (*Service, *config.SecretResolver) {
	t.Helper()
	t.Setenv("ARB_MASTER_KEY", "d545-test-master-key")
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	if err := st.DeleteAppSecret(context.Background(), "TRONGRID_API_KEY"); err != nil {
		t.Fatalf("cleanup key: %v", err)
	}
	res := config.NewSecretResolver(nil)
	return &Service{store: st, secRes: res}, res
}

func TestD545_T2_UpdateSecretCipherAndHotResolve(t *testing.T) {
	svc, res := d545Svc(t)
	ctx := context.Background()

	// 审计行抓包（key-only 闸门：值永不进日志）。
	var logs bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(orig)

	// note 必填拒。
	if _, err := svc.UpdateAppSecret(ctx, connect.NewRequest(&dashv1.UpdateAppSecretRequest{
		Key: "TRONGRID_API_KEY", Value: "v", Note: ""})); err == nil {
		t.Fatal("空 note 应被拒")
	}
	// 非白名单拒。
	if _, err := svc.UpdateAppSecret(ctx, connect.NewRequest(&dashv1.UpdateAppSecretRequest{
		Key: "ARB_MASTER_KEY", Value: "v", Note: "n"})); err == nil {
		t.Fatal("白名单外键应被拒")
	}

	// 写入。
	const plain = "plain-trongrid-value-xyz"
	resp, err := svc.UpdateAppSecret(ctx, connect.NewRequest(&dashv1.UpdateAppSecretRequest{
		Key: "TRONGRID_API_KEY", Value: plain, Note: "t2 锚"}))
	if err != nil {
		t.Fatalf("UpdateAppSecret: %v", err)
	}
	if !resp.Msg.Key.HasValue || resp.Msg.Key.ValueSource != "db" || !resp.Msg.Key.Hot {
		t.Fatalf("写后视图: %+v", resp.Msg.Key)
	}

	// T2① resolver 立即新值（无重启无延迟）。
	if got := res.Get("TRONGRID_API_KEY"); got != plain {
		t.Fatalf("resolver 未即效: got %q", got)
	}

	// T2② DB 行=密文非明文（防明文落库回归）。
	var enc []byte
	if err := svc.store.Pool().QueryRow(ctx,
		`SELECT value_enc FROM app_secrets WHERE key='TRONGRID_API_KEY'`).Scan(&enc); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	if bytes.Contains(enc, []byte(plain)) {
		t.Fatal("DB 行含明文子串——密文闸失效")
	}
	mk, _ := cryptoutil.MasterKey()
	if got, err := cryptoutil.Open(mk, enc); err != nil || got != plain {
		t.Fatalf("解密校验: got=%q err=%v", got, err)
	}

	// T2③ 审计行只含 key 不含值。
	out := logs.String()
	if !strings.Contains(out, "TRONGRID_API_KEY") {
		t.Fatalf("审计行缺 key: %q", out)
	}
	if strings.Contains(out, plain) {
		t.Fatalf("审计行泄值: %q", out)
	}
}

func TestD545_T3_EmptyClearsRow(t *testing.T) {
	svc, res := d545Svc(t)
	ctx := context.Background()
	t.Setenv("TRONGRID_API_KEY", "") // env 源隔离——清行后 source 必为 none

	if _, err := svc.UpdateAppSecret(ctx, connect.NewRequest(&dashv1.UpdateAppSecretRequest{
		Key: "TRONGRID_API_KEY", Value: "v1", Note: "set"})); err != nil {
		t.Fatalf("set: %v", err)
	}
	resp, err := svc.UpdateAppSecret(ctx, connect.NewRequest(&dashv1.UpdateAppSecretRequest{
		Key: "TRONGRID_API_KEY", Value: "", Note: "clear"}))
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if resp.Msg.Key.HasValue || resp.Msg.Key.ValueSource != "none" {
		t.Fatalf("清后视图: %+v", resp.Msg.Key)
	}
	var n int
	if err := svc.store.Pool().QueryRow(ctx,
		`SELECT count(*) FROM app_secrets WHERE key='TRONGRID_API_KEY'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("行应删: n=%d err=%v", n, err)
	}
	if res.Has("TRONGRID_API_KEY") {
		t.Fatal("resolver 未 Unset")
	}
	// List 面 has_value 翻 false。
	lst, err := svc.ListAppSecrets(ctx, connect.NewRequest(&dashv1.ListAppSecretsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, k := range lst.Msg.Keys {
		if k.Key == "TRONGRID_API_KEY" {
			found = true
			if k.HasValue {
				t.Fatalf("List has_value 未翻 false: %+v", k)
			}
			if !k.Hot {
				t.Fatalf("TRONGRID 键应标 hot: %+v", k)
			}
		}
		// 值永不回传——视图 struct 上无 value 字段由 proto 保证，仅钉 desc 非空。
		if k.Desc == "" {
			t.Fatalf("键 %s 缺中文 desc", k.Key)
		}
	}
	if !found {
		t.Fatal("List 应含全量白名单键")
	}
}
