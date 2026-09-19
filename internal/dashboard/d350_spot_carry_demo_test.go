// d350_spot_carry_demo_test.go —— D-350 T3（unit）：demo spot 端点构造（base 换段）+RPC 参数校验。
package dashboard

import (
	"context"
	"testing"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
)

// TestD350_DeriveSpotBaseURL 端点构造锚：id=25 demo perp base（…/demofapi/fapi）换 /api 段；
// 非 /fapi 尾（id=19 主网 …/api 形态）原样返回。
func TestD350_DeriveSpotBaseURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://192.229.85.35:8443/demofapi/fapi", "https://192.229.85.35:8443/demofapi/api"},
		{"https://192.229.85.35:8443/api", "https://192.229.85.35:8443/api"},
	}
	for _, c := range cases {
		if got := deriveSpotBaseURL(c.in); got != c.want {
			t.Errorf("deriveSpotBaseURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestD350_ExecuteSpotCarryDemoParamValidation RPC 参数校验（store 访问前；零值 Service 可跑）。
func TestD350_ExecuteSpotCarryDemoParamValidation(t *testing.T) {
	svc := &Service{}
	cases := []struct {
		name string
		req  v1.ExecuteSpotCarryDemoRequest
		code connect.Code
	}{
		{"venue 非法", v1.ExecuteSpotCarryDemoRequest{Venue: "okx", Symbol: "GUSDT", Side: "short_perp", QtyUsd: 100}, connect.CodeInvalidArgument},
		{"side 非法", v1.ExecuteSpotCarryDemoRequest{Venue: "binance", Symbol: "GUSDT", Side: "buy", QtyUsd: 100}, connect.CodeInvalidArgument},
		{"qty 非法", v1.ExecuteSpotCarryDemoRequest{Venue: "binance", Symbol: "GUSDT", Side: "short_perp", QtyUsd: 0}, connect.CodeInvalidArgument},
		{"symbol 空", v1.ExecuteSpotCarryDemoRequest{Venue: "binance", Symbol: " ", Side: "short_perp", QtyUsd: 100}, connect.CodeInvalidArgument},
	}
	for i := range cases {
		c := &cases[i] // 索引取址避免 range 拷贝 proto message（vet copylocks）
		_, err := svc.ExecuteSpotCarryDemo(context.Background(), connect.NewRequest(&c.req))
		if err == nil {
			t.Errorf("%s：应拒绝", c.name)
			continue
		}
		if connect.CodeOf(err) != c.code {
			t.Errorf("%s：code = %v, want %v", c.name, connect.CodeOf(err), c.code)
		}
	}
}
