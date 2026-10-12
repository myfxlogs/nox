// d104_egress_gate_test.go —— D-104②锚：canary 红窗聚合闸——红窗内 ws-storm/面判定
// 劣化聚合（true=不落库），其余消息放行；绿窗全放行（fail-open）。
package main

import (
	"testing"

	"nox/internal/risk/selfwatch"
)

func TestD104_EgressAggregated(t *testing.T) {
	selfwatch.SetEgressRed(true)
	defer selfwatch.SetEgressRed(false) // 复位防跨测试泄漏
	for _, msg := range []string{
		"[ws-storm] https://x 1 小时内重连 ≥6 次",
		"okx:perp 面判定劣化：连败 5 次且 ≥3m 无成功，末错=timeout",
	} {
		if !egressAggregated(msg) {
			t.Fatalf("红窗内应聚合: %q", msg)
		}
	}
	for _, msg := range []string{
		"[canary] 本机出口故障，所有 venue 面按 fail-closed 对待", // canary 自身条目不闸
		"binance MARGIN_CALL 保证金追缴",                  // 无关族照发
	} {
		if egressAggregated(msg) {
			t.Fatalf("非聚合族应放行: %q", msg)
		}
	}
	selfwatch.SetEgressRed(false)
	// 绿窗：全放行（canary 停跑/重启间隙同态=宁多告不漏告）。
	if egressAggregated("[ws-storm] https://x 重连风暴") {
		t.Fatal("绿窗不应聚合")
	}
}
