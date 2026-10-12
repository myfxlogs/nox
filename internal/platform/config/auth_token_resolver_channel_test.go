// d095_auth_token_channel_test.go —— D-095 锚：NOX_AUTH_TOKEN 入 app_secrets 通道
// （白名单/播种集/分组描述/hot 标注）+ resolver DB>env 热轮换语义。
package config

import "testing"

// TestD095_AuthTokenResolverChannel 令牌入通道锚：
// ① IsAppSecretKey(app_secrets 写面白名单) = true（面板可管）；
// ② AppSecretSeedKeys 含该键（首启空表播种写回 PG；PG 无值时装配方 Set env 回落仍可用）；
// ③ resolver：DB 种子胜出 env 回落；Set 即效（面板轮换写时同步）。
func TestD095_AuthTokenResolverChannel(t *testing.T) {
	if !IsAppSecretKey("NOX_AUTH_TOKEN") {
		t.Fatal("NOX_AUTH_TOKEN 应入 app_secrets 白名单（D-095 迁 PG）")
	}
	found := false
	for _, k := range AppSecretSeedKeys() {
		if k == "NOX_AUTH_TOKEN" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("NOX_AUTH_TOKEN 应入播种键集（首启写回 PG）")
	}
	t.Setenv("NOX_AUTH_TOKEN", "env-token")
	// DB 种子胜出 env 回落。
	r := NewSecretResolver(map[string]string{"NOX_AUTH_TOKEN": "db-token"})
	if got := r.Get("NOX_AUTH_TOKEN"); got != "db-token" {
		t.Fatalf("DB 种子应胜出: got %q", got)
	}
	// PG 无值 → env 回落可用（装配方 newSecretResolver 对缺键 Set(env) 同构）。
	r2 := NewSecretResolver(nil)
	r2.Set("NOX_AUTH_TOKEN", "env-token")
	if got := r2.Get("NOX_AUTH_TOKEN"); got != "env-token" {
		t.Fatalf("env 回落应可用: got %q", got)
	}
	// 轮换：Set 新值即效（无 TTL 等待）。
	r2.Set("NOX_AUTH_TOKEN", "db-new")
	if got := r2.Get("NOX_AUTH_TOKEN"); got != "db-new" {
		t.Fatalf("轮换后应即效: got %q", got)
	}
}
