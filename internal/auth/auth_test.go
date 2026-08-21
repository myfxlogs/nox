package auth

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
)

// TestInterceptor_EmptyTokenPassthrough D-073 demo 豁免：token == "" 时放行全部请求（不校验）。
// 任意 Authorization 头（含缺失/错误）都应通过——demo 阶段无真实资金，公网匿名可达。
func TestInterceptor_EmptyTokenPassthrough(t *testing.T) {
	i := NewInterceptor("")
	cases := []struct {
		name string
		auth string
	}{
		{"无 Authorization 头", ""},
		{"错误 scheme", "Basic abc"},
		{"错误 token", "Bearer wrong"},
		{"正确格式但空 token 放行", "Bearer "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			if c.auth != "" {
				h.Set("Authorization", c.auth)
			}
			if err := i.check(h); err != nil {
				t.Errorf("empty token check() err = %v, want nil（demo 豁免放行）", err)
			}
		})
	}
}

// TestInterceptor_NonEmptyTokenStillValidates D-048 回归：token 非空时行为不变（恢复=设 env 即可）。
func TestInterceptor_NonEmptyTokenStillValidates(t *testing.T) {
	i := NewInterceptor("secret-token")
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

// TestInterceptor_WrapUnaryEmptyToken D-073：空 token 时 WrapUnary 放行（不返回 Unauthenticated）。
func TestInterceptor_WrapUnaryEmptyToken(t *testing.T) {
	i := NewInterceptor("")
	called := false
	fn := i.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, nil
	})
	req := connect.NewRequest[any](nil)
	_, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("WrapUnary err = %v, want nil（空 token 放行）", err)
	}
	if !called {
		t.Error("next handler not called（空 token 应放行到 next）")
	}
}
