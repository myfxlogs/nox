package config

// 验收锚：SecretResolver——种子 DB>env 优先级/Get/Set/Unset/白名单外返 ""。

import "testing"

func TestD545_T1_Resolver(t *testing.T) {
	t.Setenv("NOX_FRED_API_KEY", "env-fred")
	// 种子=DB 解密集（模拟 boot：DB 行胜出 env 回落——env 回落由装配方显式 Set 注入）。
	r := NewSecretResolver(map[string]string{"NOX_FRED_API_KEY": "db-fred", "TRONGRID_API_KEY": "db-tron"})
	if got := r.Get("NOX_FRED_API_KEY"); got != "db-fred" {
		t.Fatalf("DB 种子应胜出: got %q", got)
	}
	if !r.Has("TRONGRID_API_KEY") {
		t.Fatal("Has(TRONGRID_API_KEY)=false")
	}
	// 白名单外键静默 ""（fail-closed）。
	if got := r.Get("NOX_MASTER_KEY"); got != "" {
		t.Fatalf("whitelist-out key got %q", got)
	}
	// 迁移：NOX_AUTH_TOKEN 已白名单内化（旧「白名单外」断言随决策推翻删除，
	// 专锚见 TestD095_AuthTokenResolverChannel）；本 resolver 种子未含该键 → ""。
	if got := r.Get("NOX_AUTH_TOKEN"); got != "" {
		t.Fatalf("无种子值应返空: got %q", got)
	}
	// Set 即时生效（UpdateAppSecret 写后同进程新值）。
	r.Set("NOX_FRED_API_KEY", "new-fred")
	if got := r.Get("NOX_FRED_API_KEY"); got != "new-fred" {
		t.Fatalf("Set 后 Get: got %q", got)
	}
	// Set 白名单外无效。
	r.Set("NOX_MASTER_KEY", "x")
	if r.Has("NOX_MASTER_KEY") {
		t.Fatal("Set whitelist-out 不应生效")
	}
	// Unset 清行；Set 空串等价清除。
	r.Set("TRONGRID_API_KEY", "")
	if r.Has("TRONGRID_API_KEY") {
		t.Fatal("Set 空串未清除")
	}
	r.Set("NOX_FRED_API_KEY", "v2")
	r.Unset("NOX_FRED_API_KEY")
	if got := r.Get("NOX_FRED_API_KEY"); got != "" {
		t.Fatalf("Unset 后 Get: got %q", got)
	}
	// 标量借轨键白名单内。
	if !secretResolverAllowed("NOX_MACRO_ENABLED") {
		t.Fatal("NOX_MACRO_ENABLED 应在 resolver 可读集")
	}
	// nil resolver fail-closed。
	var nilRes *SecretResolver
	if nilRes.Get("NOX_FRED_API_KEY") != "" || nilRes.Has("NOX_FRED_API_KEY") {
		t.Fatal("nil resolver 应返空")
	}
	nilRes.Set("NOX_FRED_API_KEY", "x") // 不 panic
	nilRes.Unset("NOX_FRED_API_KEY")
}
