package gate

import (
	"testing"

	"nox/internal/store/pgstore"
)

func f64(v float64) *float64 { return &v }

// TestCustodyShareOf_VenueGranularity D-644 双键查序锚：
// custody_share@<venue> 优先、无 venue 键回落全局——okx 池×1.0 共存
// binance 共享池×0.34 互不串（粒度修前全局单键两域混乘）。
func TestCustodyShareOf_VenueGranularity(t *testing.T) {
	cons := []pgstore.CapabilityConstraint{
		{Subject: "custody_share", Qualifier: "max", ValueNum: f64(0.34)},
		{Subject: "custody_share@okx", Qualifier: "max", ValueNum: f64(1.0)},
	}
	if got := CustodyShareOf(cons, "okx"); got == nil || *got != 1.0 {
		t.Fatalf("@okx 键应优先: %v", got)
	}
	if got := CustodyShareOf(cons, "binance"); got == nil || *got != 0.34 {
		t.Fatalf("binance 无 venue 键应回落全局 0.34: %v", got)
	}
	if got := CustodyShareOf(cons, ""); got == nil || *got != 0.34 {
		t.Fatalf("空 venue=全局键: %v", got)
	}
	// 仅 venue 键无全局——binance 域=nil（gap 径 fail-closed 同旧语义）。
	cons2 := []pgstore.CapabilityConstraint{
		{Subject: "custody_share@okx", Qualifier: "max", ValueNum: f64(1.0)},
	}
	if got := CustodyShareOf(cons2, "binance"); got != nil {
		t.Fatalf("仅 @okx 键时 binance 应 nil: %v", *got)
	}
	if got := CustodyShareOf(cons2, "okx"); got == nil || *got != 1.0 {
		t.Fatalf("仅 @okx 键时 okx 应命中: %v", got)
	}
	// 行在无数值=不命中（nil 值行不得顶替全局回落）。
	cons3 := []pgstore.CapabilityConstraint{
		{Subject: "custody_share", ValueNum: f64(0.34)},
		{Subject: "custody_share@okx"},
	}
	if got := CustodyShareOf(cons3, "okx"); got == nil || *got != 0.34 {
		t.Fatalf("@okx 无数值应回落全局: %v", got)
	}
}

// TestTenorCapOf_GlobalOnly D-644 后 TenorCapOf share=全局键语义不变锚：
// venue 键不污染全局读面（reconcile/wheel binance 域经 CustodyShareOf(binance) 取）。
func TestTenorCapOf_GlobalOnly(t *testing.T) {
	cons := []pgstore.CapabilityConstraint{
		{Subject: "custody_share", ValueNum: f64(0.34)},
		{Subject: "custody_share@okx", ValueNum: f64(1.0)},
		{Subject: "custody_redeemable", ValueNum: f64(90)},
	}
	share, cap := TenorCapOf(cons)
	if share == nil || *share != 0.34 || cap != 90 {
		t.Fatalf("全局读面漂移: share=%v cap=%d", share, cap)
	}
}
