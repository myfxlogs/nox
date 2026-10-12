package gate

// xasset_skip_notional_test.go —— D-330 挂起件回补段（M2.3 拆分后 gateDecision 非导出，
// 纯函数层断言按 D-223⑤ 函数粒度落 gate 包）：XASSET_CARRY SkipNotional=true 豁免名义帽。

import "testing"

func TestD330_GateDecisionXAssetSkipNotional(t *testing.T) {
	// gateDecision 纯函数层：SkipNotional=true 豁免名义帽（同 SWAPARB/CARRY 语义，D-187 先例）
	v := gateDecision(GateInput{
		State: StateRunning, Strategy: "XASSET_CARRY",
		MaxDailyLoss: 1000, MaxPositions: 5,
		NotionalUSD: 4355, MaxNotional: 1000, SkipNotional: true, DedupHit: false,
	})
	if !v.Pass {
		t.Errorf("gateDecision XASSET SkipNotional=true 应放行, got reject: %s", v.Reason)
	}
}
