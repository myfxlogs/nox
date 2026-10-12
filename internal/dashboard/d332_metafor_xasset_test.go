package dashboard

import (
	"math"
	"testing"
)

// D-332 T1：metaForType XASSET_CARRY 归 FUNDING_HEDGE/PERP_FUNDING 分支（NetBps=年化百分点
// → AnnualPct=NetBps 直存），绝不归 CARRY 分支（v*365/100 日 bps 口径，归错写 29.6）。
// 红=现码 default 分支 AnnualPct=nil。
func TestD332_MetaForTypeXAssetCarryFundingBranch(t *testing.T) {
	m := metaForType("XASSET_CARRY", 8.1)
	if m.NetBps == nil || math.Abs(*m.NetBps-8.1) > 1e-9 {
		t.Errorf("NetBps = %v, want 8.1", m.NetBps)
	}
	if m.AnnualPct == nil {
		t.Fatalf("AnnualPct = nil（红=default 分支漏型），want 8.1（FUNDING 分支直存）")
	}
	if math.Abs(*m.AnnualPct-8.1) > 1e-9 {
		t.Errorf("AnnualPct = %v, want 8.1", *m.AnnualPct)
	}
	// 反向锚：不得走 CARRY 分支语义（8.1×365/100=29.565）
	if math.Abs(*m.AnnualPct-8.1*365/100) < 1e-6 {
		t.Errorf("AnnualPct = %v 走了 CARRY 分支语义（v*365/100），want FUNDING 直存 8.1", *m.AnnualPct)
	}
}
