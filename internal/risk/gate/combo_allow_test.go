package gate

import (
	"strings"
	"testing"

	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// TestComboAllowVerdict_EmptyAllowlist D-159 W1a R1：空 allowlist + autoAllow=false → fail-closed。
func TestComboAllowVerdict_EmptyAllowlist(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "XAUUSD", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	v := comboAllowVerdict(nil, nil, false, 1, 0, intent, nil)
	if v.Pass {
		t.Fatal("empty allowlist with autoAllow=false should fail-closed")
	}
	if !strings.Contains(v.Reason, "no swaparb combo allowlist") {
		t.Errorf("reason want 'no swaparb combo allowlist', got %q", v.Reason)
	}
}

// TestComboAllowVerdict_NotInAllowlist D-159 W1a R1：组合不在 allowlist + autoAllow=false → 拒绝。
func TestComboAllowVerdict_NotInAllowlist(t *testing.T) {
	allow := []exec.ComboAllowSpec{
		{Canonical: "XAUUSD", VenueLong: "A:mt5", VenueShort: "B:mt5"},
	}
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "ETHUSDT", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	v := comboAllowVerdict(allow, nil, false, 1, 0, intent, nil)
	if v.Pass {
		t.Fatal("combo not in allowlist with autoAllow=false should reject")
	}
	if !strings.Contains(v.Reason, "not in allowlist") {
		t.Errorf("reason want 'not in allowlist', got %q", v.Reason)
	}
}

// TestComboAllowVerdict_Pass D-159 W1a R1：组合在 allowlist + 无 active → 放行。
func TestComboAllowVerdict_Pass(t *testing.T) {
	allow := []exec.ComboAllowSpec{
		{Canonical: "XAUUSD", VenueLong: "A:mt5", VenueShort: "B:mt5"},
	}
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "XAUUSD", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	v := comboAllowVerdict(allow, nil, false, 1, 0, intent, nil)
	if !v.Pass {
		t.Fatalf("valid combo with no active should pass, got reject: %s", v.Reason)
	}
}

// TestComboAllowVerdict_MaxActive D-159 W1a R1：active 数 ≥ MaxActive → 拒绝。
func TestComboAllowVerdict_MaxActive(t *testing.T) {
	allow := []exec.ComboAllowSpec{
		{Canonical: "XAUUSD", VenueLong: "A:mt5", VenueShort: "B:mt5"},
	}
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "XAUUSD", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:XAUUSD:A:mt5:B:mt5", Status: "OPEN"},
	}
	v := comboAllowVerdict(allow, nil, false, 1, 0, intent, active)
	if v.Pass {
		t.Fatal("active count >= maxActive should reject")
	}
	if !strings.Contains(v.Reason, "max active combos reached") {
		t.Errorf("reason want 'max active combos reached', got %q", v.Reason)
	}
}

// TestComboAllowVerdict_MaxActiveZeroDefaultsOne D-159 W1a R1：MaxActive ≤0 默认 1。
func TestComboAllowVerdict_MaxActiveZeroDefaultsOne(t *testing.T) {
	allow := []exec.ComboAllowSpec{
		{Canonical: "XAUUSD", VenueLong: "A:mt5", VenueShort: "B:mt5"},
	}
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "XAUUSD", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:XAUUSD:A:mt5:B:mt5", Status: "OPEN"},
	}
	v := comboAllowVerdict(allow, nil, false, 0, 0, intent, active)
	if v.Pass {
		t.Fatal("maxActive=0 should default to 1, reject with 1 active")
	}
}

// TestComboAllowVerdict_OldFormatOppID D-159 W1a R1：旧格式 oppID（带时间戳）也能匹配。
func TestComboAllowVerdict_OldFormatOppID(t *testing.T) {
	allow := []exec.ComboAllowSpec{
		{Canonical: "XAUUSD", VenueLong: "A:mt5", VenueShort: "B:mt5"},
	}
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "XAUUSD", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:XAUUSD:A:mt5:B:mt5:1787282156117", Status: "OPEN"},
	}
	v := comboAllowVerdict(allow, nil, false, 1, 0, intent, active)
	if v.Pass {
		t.Fatal("old format oppID active should match and reject (max active)")
	}
}

// === D-168 自动准入测试 ===

// TestComboAllowVerdict_AutoAllow_EmptyAllowlist D-168：autoAllow=true + 空白名单 → 放行。
func TestComboAllowVerdict_AutoAllow_EmptyAllowlist(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	v := comboAllowVerdict(nil, nil, true, 1, 0, intent, nil)
	if !v.Pass {
		t.Fatalf("autoAllow=true with empty allowlist should pass, got: %s", v.Reason)
	}
}

// TestComboAllowVerdict_AutoAllow_NotInAllowlist D-168：autoAllow=true + 组合不在白名单 → 仍放行。
func TestComboAllowVerdict_AutoAllow_NotInAllowlist(t *testing.T) {
	allow := []exec.ComboAllowSpec{
		{Canonical: "XAUUSD", VenueLong: "A:mt5", VenueShort: "B:mt5"},
	}
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	v := comboAllowVerdict(allow, nil, true, 1, 0, intent, nil)
	if !v.Pass {
		t.Fatalf("autoAllow=true with combo not in allowlist should still pass, got: %s", v.Reason)
	}
}

// TestComboAllowVerdict_DenyList D-168：黑名单组合一律拒绝（即使 autoAllow=true）。
func TestComboAllowVerdict_DenyList(t *testing.T) {
	deny := []exec.ComboAllowSpec{
		{Canonical: "WTI", VenueLong: "A:mt5", VenueShort: "B:mt5"},
	}
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "WTI", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	v := comboAllowVerdict(nil, deny, true, 1, 0, intent, nil)
	if v.Pass {
		t.Fatal("combo in denylist should reject even with autoAllow=true")
	}
	if !strings.Contains(v.Reason, "denylist") {
		t.Errorf("reason want 'denylist', got %q", v.Reason)
	}
}

// TestComboAllowVerdict_DenyListPriority D-168：黑名单优先于白名单（在白名单但也在黑名单 → 拒绝）。
func TestComboAllowVerdict_DenyListPriority(t *testing.T) {
	allow := []exec.ComboAllowSpec{
		{Canonical: "WTI", VenueLong: "A:mt5", VenueShort: "B:mt5"},
	}
	deny := []exec.ComboAllowSpec{
		{Canonical: "WTI", VenueLong: "A:mt5", VenueShort: "B:mt5"},
	}
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "WTI", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	v := comboAllowVerdict(allow, deny, false, 1, 0, intent, nil)
	if v.Pass {
		t.Fatal("combo in both allowlist and denylist should reject (deny priority)")
	}
}

// TestComboAllowVerdict_GrossExposure D-179 S3：全局额度超限 → 折分放行（AdjustedNotional=remaining/2 单腿）。
// activeGross=$3000（双腿口径），remaining=$5000-$3000=$2000（双腿），$2000 < $3000×2=$6000 → 折分。
// AdjustedNotional=$2000/2=$1000（单腿目标）。
func TestComboAllowVerdict_GrossExposure(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 3000}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:BRENT:C:mt5:D:mt5", Status: "OPEN", NotionalUSD: ptrFloat(3000)},
	}
	v := comboAllowVerdict(nil, nil, true, 5, 5000, intent, active)
	if !v.Pass {
		t.Fatalf("gross exposure exceeded should split (pass), got reject: %s", v.Reason)
	}
	if v.AdjustedNotional != 1000 {
		t.Errorf("AdjustedNotional want 1000 (remaining/2), got %v", v.AdjustedNotional)
	}
}

// TestComboAllowVerdict_GrossExposureExhausted D-179 S3：全局额度耗尽（remaining<=0）→ 拒绝。
// activeGross=$6000（2 仓 × $3000 双腿），remaining=$5000-$6000=-$1000 <= 0 → 拒绝。
func TestComboAllowVerdict_GrossExposureExhausted(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 3000}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:BRENT:C:mt5:D:mt5", Status: "OPEN", NotionalUSD: ptrFloat(3000)},
		{OpportunityID: "SWAPARB:WTI:E:mt5:F:mt5", Status: "OPEN", NotionalUSD: ptrFloat(3000)},
	}
	v := comboAllowVerdict(nil, nil, true, 5, 5000, intent, active)
	if v.Pass {
		t.Fatal("gross exposure exhausted should reject")
	}
	if !strings.Contains(v.Reason, "gross exposure exhausted") {
		t.Errorf("reason want 'gross exposure exhausted', got %q", v.Reason)
	}
}

// TestComboAllowVerdict_GrossExposureWithin D-179 S3：全局额度内 → 放行。
// activeGross=$2000（双腿），remaining=$3000，$3000 >= $2000×2=$4000? No → $3000 < $4000 → 折分。
// 修正：activeGross=$2000，remaining=$3000，intent.NotionalUSD×2=$4000 > $3000 → 折分。
// 要整放需 remaining >= NotionalUSD×2。设 activeGross=$1000，remaining=$4000，$4000 >= $2000×2=$4000 → 整放。
func TestComboAllowVerdict_GrossExposureWithin(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 2000}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:BRENT:C:mt5:D:mt5", Status: "OPEN", NotionalUSD: ptrFloat(1000)},
	}
	// activeGross=$1000，remaining=$4000，$4000 >= $2000×2=$4000 → 整放（不折分）
	v := comboAllowVerdict(nil, nil, true, 5, 5000, intent, active)
	if !v.Pass {
		t.Fatalf("gross exposure within limit should pass, got: %s", v.Reason)
	}
	if v.AdjustedNotional != 0 {
		t.Errorf("within limit should not split, AdjustedNotional want 0, got %v", v.AdjustedNotional)
	}
}

// TestComboAllowVerdict_GrossExposureZero D-168：maxGrossExposure=0 → 不校验（放行）。
func TestComboAllowVerdict_GrossExposureZero(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 999999}
	v := comboAllowVerdict(nil, nil, true, 5, 0, intent, nil)
	if !v.Pass {
		t.Fatalf("maxGrossExposure=0 should not check, got: %s", v.Reason)
	}
}

// === D-179 S3 新增五组单测 ===

// ptrFloat 测试助手：取 float64 指针。
func ptrFloat(v float64) *float64 { return &v }

// TestComboAllowVerdict_GrossRealSum D-179 S3：大名义机会不再产生 $150K 级荒谬估值。
// 旧公式：3 仓 × $50000 = $150K → 全拒。新公式：Σ真实 notional=$7440 → remaining 正常。
func TestComboAllowVerdict_GrossRealSum(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "GOLD", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 50000}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:AUDJPY:A:B", Status: "OPEN", NotionalUSD: ptrFloat(2480)},
		{OpportunityID: "SWAPARB:GBPJPY:C:D", Status: "OPEN", NotionalUSD: ptrFloat(2480)},
		{OpportunityID: "SWAPARB:EURJPY:E:F", Status: "OPEN", NotionalUSD: ptrFloat(2480)},
	}
	// activeGross=$7440，maxGross=$10000，remaining=$2560 < $50000×2=$100000 → 折分
	v := comboAllowVerdict(nil, nil, true, 5, 10000, intent, active)
	if !v.Pass {
		t.Fatalf("real sum should split not reject, got: %s", v.Reason)
	}
	if v.AdjustedNotional != 1280 { // $2560/2
		t.Errorf("AdjustedNotional want 1280, got %v", v.AdjustedNotional)
	}
}

// TestComboAllowVerdict_GrossNullCountsZero D-179 S3：NULL notional 计 0（不 panic 不阻塞）。
func TestComboAllowVerdict_GrossNullCountsZero(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 2000}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:BRENT:C:mt5:D:mt5", Status: "OPEN", NotionalUSD: nil},
	}
	// activeGross=0（nil 计 0），remaining=$5000 >= $2000×2=$4000 → 整放
	v := comboAllowVerdict(nil, nil, true, 5, 5000, intent, active)
	if !v.Pass {
		t.Fatalf("NULL notional counts 0 should pass, got: %s", v.Reason)
	}
}

// TestComboAllowVerdict_GrossSplitBoundary D-179 S3：折分边界——remaining=双腿名义（恰好整放）。
// activeGross=$6000，remaining=$4000，intent.NotionalUSD×2=$4000 → remaining >= NotionalUSD×2 → 整放（边界=不折分）。
func TestComboAllowVerdict_GrossSplitBoundary(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 2000}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:X:A:B", Status: "OPEN", NotionalUSD: ptrFloat(6000)},
	}
	v := comboAllowVerdict(nil, nil, true, 5, 10000, intent, active)
	if !v.Pass {
		t.Fatalf("boundary should pass, got: %s", v.Reason)
	}
	if v.AdjustedNotional != 0 {
		t.Errorf("boundary remaining==NotionalUSD×2 should not split, got AdjustedNotional=%v", v.AdjustedNotional)
	}
}

// TestComboAllowVerdict_GrossSplitBelowBoundary D-179 S3：折分边界——remaining<双腿名义（折分）。
// activeGross=$7000，remaining=$3000，intent.NotionalUSD×2=$4000 → $3000 < $4000 → 折分。
// AdjustedNotional=$3000/2=$1500（单腿目标）。
func TestComboAllowVerdict_GrossSplitBelowBoundary(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 2000}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:X:A:B", Status: "OPEN", NotionalUSD: ptrFloat(7000)},
	}
	v := comboAllowVerdict(nil, nil, true, 5, 10000, intent, active)
	if !v.Pass {
		t.Fatalf("below boundary should split (pass), got: %s", v.Reason)
	}
	if v.AdjustedNotional != 1500 {
		t.Errorf("AdjustedNotional want 1500, got %v", v.AdjustedNotional)
	}
}

// TestComboAllowVerdict_GrossRemainingLeZero D-179 S3：折分边界——remaining<=0（拒绝）。
func TestComboAllowVerdict_GrossRemainingLeZero(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 2000}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:X:A:B", Status: "OPEN", NotionalUSD: ptrFloat(10000)},
	}
	v := comboAllowVerdict(nil, nil, true, 5, 10000, intent, active)
	if v.Pass {
		t.Fatal("remaining<=0 should reject")
	}
	if !strings.Contains(v.Reason, "gross exposure exhausted") {
		t.Errorf("reason want 'gross exposure exhausted', got %q", v.Reason)
	}
}

// TestComboAllowVerdict_GrossJpyCrossRegression D-179 S3 v2-F 口径回归：
// GBPJPY 场景：单腿 $1470、activeGross=$7440、maxGross=$10000 → remaining=$2560（双腿）。
// $2560 < $1470×2=$2940 → 必须折分（非整放）。AdjustedNotional=$2560/2=$1280（单腿）。
// 旧 v1-C 漏改口径会：remaining=$2560 vs NotionalUSD=$1470 → $2560>$1470 整放 → 双腿落盘 $2940 → Σ 冲到 $10380>$10000。
func TestComboAllowVerdict_GrossJpyCrossRegression(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "GBPJPY", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 1470}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:AUDJPY:A:B", Status: "OPEN", NotionalUSD: ptrFloat(2480)},
		{OpportunityID: "SWAPARB:GBPJPY:C:D", Status: "OPEN", NotionalUSD: ptrFloat(2480)},
		{OpportunityID: "SWAPARB:EURJPY:E:F", Status: "OPEN", NotionalUSD: ptrFloat(2480)},
	}
	// activeGross=$7440，remaining=$2560，$2560 < $1470×2=$2940 → 折分
	v := comboAllowVerdict(nil, nil, true, 5, 10000, intent, active)
	if !v.Pass {
		t.Fatalf("JPY cross regression should split (pass), got: %s", v.Reason)
	}
	if v.AdjustedNotional != 1280 { // $2560/2
		t.Errorf("AdjustedNotional want 1280, got %v (口径换算缺失？)", v.AdjustedNotional)
	}
}

// TestComboAllowVerdict_GrossNonSwapArbExcluded D-179 S3：Σ 含非 SWAPARB 仓不计入。
func TestComboAllowVerdict_GrossNonSwapArbExcluded(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 2000}
	active := []pgstore.Position{
		{OpportunityID: "CROSS_VENUE:BTCUSDT:A:B", Status: "OPEN", NotionalUSD: ptrFloat(50000)},
		{OpportunityID: "SWAPARB:X:A:B", Status: "OPEN", NotionalUSD: ptrFloat(1000)},
	}
	// activeGross=$1000（只计 SWAPARB，CROSS_VENUE $50000 不计入），remaining=$4000 >= $4000 → 整放
	v := comboAllowVerdict(nil, nil, true, 5, 5000, intent, active)
	if !v.Pass {
		t.Fatalf("non-SWAPARB should be excluded, got: %s", v.Reason)
	}
	if v.AdjustedNotional != 0 {
		t.Errorf("within limit should not split, got AdjustedNotional=%v", v.AdjustedNotional)
	}
}
