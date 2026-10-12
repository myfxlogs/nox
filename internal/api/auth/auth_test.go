package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

// TestInterceptor_EmptyTokenFailClosed D-159 W6a S1：token == "" 时 fail-closed（移除逃生门后）。
// 任意 Authorization 头（含缺失/错误）都应返回 error——生产 token 必须非空（ValidateStartupToken 保证启动时非空）。
func TestInterceptor_EmptyTokenFailClosed(t *testing.T) {
	i := NewInterceptor(func() string { return "" })
	cases := []struct {
		name string
		auth string
	}{
		{"无 Authorization 头 → Unauthenticated", ""},
		{"错误 scheme → Unauthenticated", "Basic abc"},
		{"错误 token → Unauthenticated", "Bearer wrong"},
		{"正确格式但空 token → Unauthenticated", "Bearer "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			if c.auth != "" {
				h.Set("Authorization", c.auth)
			}
			if err := i.check(h); err == nil {
				t.Errorf("empty token check() err = nil, want error（fail-closed，W6a S1 移除逃生门）")
			}
		})
	}
}

// TestInterceptor_NonEmptyTokenStillValidates D-048 回归：token 非空时行为不变（恢复=设 env 即可）。
func TestInterceptor_NonEmptyTokenStillValidates(t *testing.T) {
	i := NewInterceptor(func() string { return "secret-token" })
	// 正确 token 放行
	h := http.Header{}
	h.Set("Authorization", "Bearer secret-token")
	if err := i.check(h); err != nil {
		t.Errorf("correct token check() err = %v, want nil", err)
	}
	// 缺失头 → 401
	if err := i.check(http.Header{}); err == nil {
		t.Error("missing header check() err = nil, want error")
	}
	// 错误 token → 401
	h2 := http.Header{}
	h2.Set("Authorization", "Bearer wrong")
	if err := i.check(h2); err == nil {
		t.Error("wrong token check() err = nil, want error")
	}
	// 错误 scheme → 401
	h3 := http.Header{}
	h3.Set("Authorization", "Basic secret-token")
	if err := i.check(h3); err == nil {
		t.Error("wrong scheme check() err = nil, want error")
	}
}

// TestInterceptor_WrapUnaryEmptyToken D-159 W6a S1：空 token 时 WrapUnary fail-closed（返回 Unauthenticated）。
func TestInterceptor_WrapUnaryEmptyToken(t *testing.T) {
	i := NewInterceptor(func() string { return "" })
	called := false
	fn := i.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, nil
	})
	req := connect.NewRequest[any](nil)
	_, err := fn(context.Background(), req)
	if err == nil {
		t.Fatal("WrapUnary err = nil, want Unauthenticated（空 token fail-closed，W6a S1）")
	}
	if called {
		t.Error("next handler called（空 token 应被拦截，不调用 next）")
	}
}

// TestValidateStartupToken D-159 W0 P0-6：启动 token 校验行为矩阵。
func TestValidateStartupToken(t *testing.T) {
	cases := []struct {
		name       string
		token      string
		allowEmpty string
		wantErr    bool
	}{
		{"空 token 无逃生门 → 拒绝", "", "", true},
		{"空 token 逃生门非 true → 拒绝", "", "false", true},
		{"空 token 逃生门 true → 放行", "", "true", false},
		{"非空 token 无逃生门 → 放行", "secret", "", false},
		{"非空 token 有逃生门 → 放行", "secret", "true", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateStartupToken(c.token, c.allowEmpty)
			if c.wantErr && err == nil {
				t.Error("ValidateStartupToken err = nil, want error")
			}
			if !c.wantErr && err != nil {
				t.Errorf("ValidateStartupToken err = %v, want nil", err)
			}
		})
	}
}

// TestInterceptor_StreamingHandler D-159 W0：streaming handler 拦截测试矩阵。
func TestInterceptor_StreamingHandler(t *testing.T) {
	t.Run("非空 token 缺 header → 401", func(t *testing.T) {
		i := NewInterceptor(func() string { return "secret-token" })
		fn := i.WrapStreamingHandler(func(ctx context.Context, w connect.StreamingHandlerConn) error {
			t.Error("next handler called, want blocked")
			return nil
		})
		_ = fn(context.Background(), &fakeStreamingConn{header: http.Header{}})
	})

	t.Run("非空 token 正确 token → 放行", func(t *testing.T) {
		i := NewInterceptor(func() string { return "secret-token" })
		called := false
		fn := i.WrapStreamingHandler(func(ctx context.Context, w connect.StreamingHandlerConn) error {
			called = true
			return nil
		})
		h := http.Header{}
		h.Set("Authorization", "Bearer secret-token")
		_ = fn(context.Background(), &fakeStreamingConn{header: h})
		if !called {
			t.Error("next handler not called（正确 token 应放行）")
		}
	})

	t.Run("空 token → fail-closed（W6a S1）", func(t *testing.T) {
		i := NewInterceptor(func() string { return "" })
		called := false
		fn := i.WrapStreamingHandler(func(ctx context.Context, w connect.StreamingHandlerConn) error {
			called = true
			return nil
		})
		err := fn(context.Background(), &fakeStreamingConn{header: http.Header{}})
		if err == nil {
			t.Error("streaming handler err = nil, want Unauthenticated（空 token fail-closed）")
		}
		if called {
			t.Error("next handler called（空 token 应被拦截）")
		}
	})
}

// TestInterceptor_CheckNoTokenLeak D-159 W0：错误路径不泄露 token 值。
func TestInterceptor_CheckNoTokenLeak(t *testing.T) {
	secret := "super-secret-token-do-not-leak"
	i := NewInterceptor(func() string { return secret })
	// 缺失 header
	err := i.check(http.Header{})
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Errorf("error message leaks token: %s", err.Error())
	}
	// 错误 scheme
	h := http.Header{}
	h.Set("Authorization", "Basic "+secret)
	err = i.check(h)
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Errorf("error message leaks token: %s", err.Error())
	}
	// 错误 token
	h2 := http.Header{}
	h2.Set("Authorization", "Bearer wrong")
	err = i.check(h2)
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Errorf("error message leaks token: %s", err.Error())
	}
}

// fakeStreamingConn 实现 connect.StreamingHandlerConn 接口用于测试。
type fakeStreamingConn struct {
	header http.Header
}

func (f *fakeStreamingConn) RequestHeader() http.Header   { return f.header }
func (f *fakeStreamingConn) Send(any) error               { return nil }
func (f *fakeStreamingConn) Receive(any) error            { return nil }
func (f *fakeStreamingConn) ResponseHeader() http.Header  { return http.Header{} }
func (f *fakeStreamingConn) ResponseTrailer() http.Header { return http.Header{} }
func (f *fakeStreamingConn) Spec() connect.Spec           { return connect.Spec{} }
func (f *fakeStreamingConn) Peer() connect.Peer           { return connect.Peer{} }

// TestD095_HotRotation 热轮换锚：令牌读口换值后新令牌过、旧令牌拒（写时同步语义，
// 无 TTL 等待——D-545 SecretResolver Set 即效，main 闭包同构）。
func TestD095_HotRotation(t *testing.T) {
	cur := "token-v1"
	i := NewInterceptor(func() string { return cur })
	hOld := http.Header{}
	hOld.Set("Authorization", "Bearer token-v1")
	if err := i.check(hOld); err != nil {
		t.Fatalf("轮换前旧令牌应放行: %v", err)
	}
	cur = "token-v2" // 面板轮换=resolver.Set（读口换值）
	hNew := http.Header{}
	hNew.Set("Authorization", "Bearer token-v2")
	if err := i.check(hNew); err != nil {
		t.Fatalf("轮换后新令牌应放行: %v", err)
	}
	if err := i.check(hOld); err == nil {
		t.Fatal("轮换后旧令牌应拒（即失效）")
	}
}

// TestD095_BothEmptyFailClosed 双空 fail-closed 锚：读口返回空（PG+env 皆无）→ 全拒
// （含此前合法的旧令牌——空=未配置认证，禁放行）；nil 读口同判。
func TestD095_BothEmptyFailClosed(t *testing.T) {
	cur := "token-v1"
	i := NewInterceptor(func() string { return cur })
	h := http.Header{}
	h.Set("Authorization", "Bearer token-v1")
	if err := i.check(h); err != nil {
		t.Fatalf("前置：合法令牌应放行: %v", err)
	}
	cur = "" // PG 行删+env 无（UpdateAppSecret 空值=删行→Unset 同型）
	if err := i.check(h); err == nil {
		t.Fatal("双空应全拒（fail-closed 禁放行）")
	}
	in := NewInterceptor(nil)
	if err := in.check(h); err == nil {
		t.Fatal("nil 读口应全拒（fail-closed）")
	}
}
