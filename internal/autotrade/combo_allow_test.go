package autotrade

import (
	"strings"
	"testing"

	"arb/internal/exec"
	"arb/internal/store/pgstore"
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

// TestComboAllowVerdict_GrossExposure D-168：全局额度超限 → 拒绝。
func TestComboAllowVerdict_GrossExposure(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 3000}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:BRENT:C:mt5:D:mt5", Status: "OPEN"},
	}
	// 活跃 1 个 SWAPARB × $3000 = $3000 + 新 $3000 = $6000 > $5000
	v := comboAllowVerdict(nil, nil, true, 5, 5000, intent, active)
	if v.Pass {
		t.Fatal("gross exposure exceeded should reject")
	}
	if !strings.Contains(v.Reason, "gross exposure exceeded") {
		t.Errorf("reason want 'gross exposure exceeded', got %q", v.Reason)
	}
}

// TestComboAllowVerdict_GrossExposureWithin D-168：全局额度内 → 放行。
func TestComboAllowVerdict_GrossExposureWithin(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 2000}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:BRENT:C:mt5:D:mt5", Status: "OPEN"},
	}
	// 活跃 1 个 × $2000 = $2000 + 新 $2000 = $4000 < $5000
	v := comboAllowVerdict(nil, nil, true, 5, 5000, intent, active)
	if !v.Pass {
		t.Fatalf("gross exposure within limit should pass, got: %s", v.Reason)
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
