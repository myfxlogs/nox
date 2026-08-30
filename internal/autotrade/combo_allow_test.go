package autotrade

import (
	"strings"
	"testing"

	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// TestComboAllowVerdict_EmptyAllowlist D-159 W1a R1：空 allowlist → fail-closed。
func TestComboAllowVerdict_EmptyAllowlist(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "XAUUSD", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	v := comboAllowVerdict(nil, 1, intent, nil)
	if v.Pass {
		t.Fatal("empty allowlist should fail-closed")
	}
	if !strings.Contains(v.Reason, "no swaparb combo allowlist") {
		t.Errorf("reason want 'no swaparb combo allowlist', got %q", v.Reason)
	}
}

// TestComboAllowVerdict_NotInAllowlist D-159 W1a R1：组合不在 allowlist → 拒绝。
func TestComboAllowVerdict_NotInAllowlist(t *testing.T) {
	allow := []exec.ComboAllowSpec{
		{Canonical: "XAUUSD", VenueLong: "A:mt5", VenueShort: "B:mt5"},
	}
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "ETHUSDT", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	v := comboAllowVerdict(allow, 1, intent, nil)
	if v.Pass {
		t.Fatal("combo not in allowlist should reject")
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
	v := comboAllowVerdict(allow, 1, intent, nil)
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
	v := comboAllowVerdict(allow, 1, intent, active)
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
	v := comboAllowVerdict(allow, 0, intent, active)
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
	v := comboAllowVerdict(allow, 1, intent, active)
	if v.Pass {
		t.Fatal("old format oppID active should match and reject (max active)")
	}
}
