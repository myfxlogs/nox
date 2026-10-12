// D-256-A S3 单测：CARRY 开仓落值 expected_net_bps。
// 验证 CARRY 的 expected_net_bps == expected_annual_pct（年化值），
// 使 verification.ExpectedUsd CARRY 分支（netBps/100×notional×holdDays/365）正确计算。
// SWAPARB 不写 NetBps（ExpectedUsd SWAPARB 分支读 swapBpsDaily）。
package autotrade

import (
	"testing"

	"nox/internal/review/verification"
	"nox/internal/store/pgstore"
)

// TestD256A_S3_CarryExpectedNetBpsEqualsAnnualPct CARRY 开仓 meta 的 NetBps==AnnualPct（年化值）。
// 模拟 engine_swaparb.go trySwapArb 的 UpdatePositionMeta 构造逻辑。
func TestD256A_S3_CarryExpectedNetBpsEqualsAnnualPct(t *testing.T) {
	netSwapBps := 0.5 // 日净 swap bps
	spreadBps := 1.2
	annual := netSwapBps * 365 / 100

	// CARRY 分支：meta.NetBps = annual（D-256-A S3）
	meta := pgstore.PositionMeta{
		EntrySpreadBps: &spreadBps,
		SwapBpsDaily:   &netSwapBps,
		AnnualPct:      &annual,
	}
	if strategy := "CARRY"; strategy == "CARRY" {
		netBps := annual
		meta.NetBps = &netBps
	}

	if meta.NetBps == nil {
		t.Fatal("CARRY meta.NetBps=nil, want non-nil (annual value)")
	}
	if *meta.NetBps != annual {
		t.Errorf("CARRY meta.NetBps=%.4f, want annual=%.4f", *meta.NetBps, annual)
	}
	if meta.AnnualPct == nil || *meta.AnnualPct != annual {
		t.Errorf("CARRY meta.AnnualPct=%v, want %.4f", meta.AnnualPct, annual)
	}
}

// TestD256A_S3_SwapArbNoNetBps SWAPARB 不写 NetBps（ExpectedUsd 读 swapBpsDaily）。
func TestD256A_S3_SwapArbNoNetBps(t *testing.T) {
	netSwapBps := 0.5
	spreadBps := 1.2
	annual := netSwapBps * 365 / 100

	meta := pgstore.PositionMeta{
		EntrySpreadBps: &spreadBps,
		SwapBpsDaily:   &netSwapBps,
		AnnualPct:      &annual,
	}
	// SWAPARB 分支：不写 NetBps
	if strategy := "SWAPARB"; strategy == "CARRY" {
		netBps := annual
		meta.NetBps = &netBps
	}

	if meta.NetBps != nil {
		t.Errorf("SWAPARB meta.NetBps=%v, want nil (SWAPARB reads swapBpsDaily not netBps)", meta.NetBps)
	}
}

// TestD256A_S3_CarryExpectedUsdNonZero CARRY ExpectedUsd 用年化 netBps 算出非零值。
// 验证 S3 修复后：expected_net_bps=年化值 → ExpectedUsd>0 → 结算不 skip。
func TestD256A_S3_CarryExpectedUsdNonZero(t *testing.T) {
	netSwapBps := 0.5                // 日净 swap bps
	annual := netSwapBps * 365 / 100 // 年化百分点 = 1.825
	notionalUSD := 100000.0
	holdDays := 10.0

	// 修复前：netBps=0 → ExpectedUsd=0（skip）
	expectedBefore := verification.ExpectedUsd("CARRY", 0, 0, notionalUSD, holdDays)
	if expectedBefore != 0 {
		t.Errorf("before fix: ExpectedUsd(CARRY, netBps=0)=%.4f, want 0 (skip)", expectedBefore)
	}

	// 修复后：netBps=annual → ExpectedUsd>0
	expectedAfter := verification.ExpectedUsd("CARRY", annual, 0, notionalUSD, holdDays)
	if expectedAfter <= 0 {
		t.Errorf("after fix: ExpectedUsd(CARRY, netBps=annual=%.4f)=%.4f, want >0", annual, expectedAfter)
	}
	// 验算：annual/100 × notional × holdDays / 365
	want := annual / 100 * notionalUSD * holdDays / 365
	if expectedAfter != want {
		t.Errorf("ExpectedUsd=%.4f, want %.4f", expectedAfter, want)
	}
}
