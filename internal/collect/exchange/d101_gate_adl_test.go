// d101_gate_adl_test.go —— D-101 锚：gate futures.adl_warning（合约级市场广播）
// 持仓关联过滤矩阵——queued/deleveraged 事实态命中持仓才 critical；warning 属预告
// 非事实恒 info；非持仓合约零 critical（锚③）；探针未接线/帧缺合约 fail-safe 视同
// 命中（宁多告不漏告）。
package exchange

import (
	"context"
	"strings"
	"testing"

	"arb/internal/bus"
)

func d101Dispatch(t *testing.T, contract, state string, hit *bool) (level, msg string) {
	t.Helper()
	g := &GateWebSocket{cfg: Config{
		AlertFn: func(l, m string) { level, msg = l, m },
	}}
	if hit != nil {
		g.cfg.ADLPositionHit = func(string) bool { return *hit }
	}
	frame := `{"channel":"futures.adl_warning","event":"update","result":{"state":"` + state + `"`
	if contract != "" {
		frame += `,"contract":"` + contract + `"`
	}
	frame += `}}`
	g.handleFuturesDispatch(context.Background(), []byte(frame), func(bus.Quote) {})
	return level, msg
}

// TestD101_ADLNonHitNoCritical 锚③：非持仓合约任意 state → 零 critical，降 info 记档
// （告警面板可查，消息带 state+持仓关联=否）。
func TestD101_ADLNonHitNoCritical(t *testing.T) {
	no := false
	for _, state := range []string{"warning", "queued", "deleveraged"} {
		level, msg := d101Dispatch(t, "STAMP_USDT", state, &no)
		if level != "info" {
			t.Fatalf("非持仓 %s 应 info 零 critical: level=%q", state, level)
		}
		if !containsAll(msg, "state="+state, "持仓关联=否", "STAMP_USDT") {
			t.Fatalf("msg 缺关联要素: %q", msg)
		}
	}
}

// TestD101_ADLHitFactCritical 锚②：queued/deleveraged 事实态且命中持仓 → critical。
func TestD101_ADLHitFactCritical(t *testing.T) {
	yes := true
	for _, state := range []string{"queued", "deleveraged"} {
		level, msg := d101Dispatch(t, "XAUT_USDT", state, &yes)
		if level != "critical" {
			t.Fatalf("命中持仓 %s 应 critical: level=%q", state, level)
		}
		if !containsAll(msg, "持仓关联=是", "XAUT_USDT") {
			t.Fatalf("msg 缺关联要素: %q", msg)
		}
	}
}

// TestD101_ADLHitWarningInfo ②前半：warning 属预告非事实——命中持仓也不升 critical。
func TestD101_ADLHitWarningInfo(t *testing.T) {
	yes := true
	level, msg := d101Dispatch(t, "XAUT_USDT", "warning", &yes)
	if level != "info" || !containsAll(msg, "持仓关联=是") {
		t.Fatalf("命中持仓 warning 应 info: level=%q msg=%q", level, msg)
	}
}

// TestD101_ADLFailSafeUncertain 探针未接线/帧缺合约=关联不明 → fail-safe 视同命中
// （事实态 critical 宁多告不漏告；warning 仍 info——预告非事实与关联无关）。
func TestD101_ADLFailSafeUncertain(t *testing.T) {
	// 探针未接线：queued/deleveraged → critical。
	level, _ := d101Dispatch(t, "BTC_USDT", "queued", nil)
	if level != "critical" {
		t.Fatalf("未接线 fail-safe queued 应 critical: %q", level)
	}
	// 探针未接线：warning → info。
	level, _ = d101Dispatch(t, "BTC_USDT", "warning", nil)
	if level != "info" {
		t.Fatalf("未接线 warning 应 info: %q", level)
	}
	// 探针接线但帧缺 contract：queued → critical（fail-safe）。
	no := false
	level, _ = d101Dispatch(t, "", "queued", &no)
	if level != "critical" {
		t.Fatalf("帧缺合约 fail-safe queued 应 critical: %q", level)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
