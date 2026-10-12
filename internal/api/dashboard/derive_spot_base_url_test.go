// d350_spot_carry_demo_test.go —— D-350 T3（unit）：demo spot 端点构造（base 换段）+RPC 参数校验。
package dashboard

import (
	"context"
	"testing"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"

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

// TestD350R1_TR1_PickSimulatedConfig P0：配置选择必须命中 simulated=true（demo）——
// created_at 序下 id=20 主网 perp 先于 id=25 demo，无 Simulated 过滤即实盘混选（fail-dangerous）。
func TestD350R1_TR1_PickSimulatedConfig(t *testing.T) {
	cfgs := []pgstore.ExchangeConfig{
		{ID: 20, Venue: "binance", VenueType: "perp", Simulated: false}, // 主网（created_at 在前）
		{ID: 25, Venue: "binance", VenueType: "perp", Simulated: true},  // demo
	}
	got := pickSimulatedConfig(cfgs, "binance", "perp")
	if got == nil || got.ID != 25 {
		t.Errorf("pick = %+v, want id=25（红=P0 实盘混选变形）", got)
	}
	if pickSimulatedConfig(cfgs, "binance", "spot") != nil {
		t.Error("无 demo（simulated=true）行应返回 nil（RPC 报 failed_precondition）")
	}
	if pickSimulatedConfig(nil, "binance", "perp") != nil {
		t.Error("空配置表应返回 nil")
	}
}
