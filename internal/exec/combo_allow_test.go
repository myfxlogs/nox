package exec

import (
	"testing"
)

// TestParseComboAllow D-159 W1a R1：解析 ARB_SWAPARB_ALLOW_COMBOS。
func TestParseComboAllow(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{"empty → nil", "", 0},
		{"whitespace → nil", "  ", 0},
		{"single valid", "XAUUSD|A:mt5|B:mt5|1.0|1.0|1000", 1},
		{"two records semicolon", "XAUUSD|A:mt5|B:mt5|1.0|1.0|1000;ETHUSDT|C:mt5|D:mt5|2.0|2.0|2000", 2},
		{"empty fields → skip", "XAUUSD|A:mt5|B:mt5|1.0|1.0|1000;|||0|0|0", 1},
		{"wrong field count → skip", "XAUUSD|A:mt5|B:mt5|1.0|1.0", 0},
		{"empty qty = unlimited", "XAUUSD|A:mt5|B:mt5|||", 1},
		{"trailing semicolon", "XAUUSD|A:mt5|B:mt5|1.0|1.0|1000;", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseComboAllow(tt.raw)
			if len(got) != tt.want {
				t.Errorf("ParseComboAllow(%q) got %d records, want %d", tt.raw, len(got), tt.want)
			}
		})
	}
}

// TestParseComboAllow_Fields D-159 W1a R1：字段解析正确性。
func TestParseComboAllow_Fields(t *testing.T) {
	allow := ParseComboAllow("XAUUSD|277842155:mt5|REDACTED_MT_ACCT:mt5|1.5|2.0|5000")
	if len(allow) != 1 {
		t.Fatalf("want 1 record, got %d", len(allow))
	}
	a := allow[0]
	if a.Canonical != "XAUUSD" {
		t.Errorf("Canonical want XAUUSD, got %s", a.Canonical)
	}
	if a.VenueLong != "277842155:mt5" {
		t.Errorf("VenueLong want 277842155:mt5, got %s", a.VenueLong)
	}
	if a.VenueShort != "REDACTED_MT_ACCT:mt5" {
		t.Errorf("VenueShort want REDACTED_MT_ACCT:mt5, got %s", a.VenueShort)
	}
	if a.MaxLongQty != 1.5 {
		t.Errorf("MaxLongQty want 1.5, got %v", a.MaxLongQty)
	}
	if a.MaxShortQty != 2.0 {
		t.Errorf("MaxShortQty want 2.0, got %v", a.MaxShortQty)
	}
	if a.MaxGrossExposureUSD != 5000 {
		t.Errorf("MaxGrossExposureUSD want 5000, got %v", a.MaxGrossExposureUSD)
	}
}

// TestMatchComboAllow D-159 W1a R1：匹配逻辑。
func TestMatchComboAllow(t *testing.T) {
	allow := ParseComboAllow("XAUUSD|A:mt5|B:mt5|1.0|1.0|1000;ETHUSDT|C:mt5|D:mt5|2.0|2.0|2000")
	tests := []struct {
		canonical, venueBuy, venueSell string
		want                           bool
	}{
		{"XAUUSD", "A:mt5", "B:mt5", true},
		{"ETHUSDT", "C:mt5", "D:mt5", true},
		{"XAUUSD", "B:mt5", "A:mt5", false},  // 顺序反
		{"BTCUSDT", "A:mt5", "B:mt5", false}, // canonical 不在
		{"XAUUSD", "A:mt5", "C:mt5", false},  // venueSell 不匹配
	}
	for _, tt := range tests {
		got := MatchComboAllow(allow, tt.canonical, tt.venueBuy, tt.venueSell)
		if (got != nil) != tt.want {
			t.Errorf("MatchComboAllow(%s,%s,%s) want match=%v, got %v", tt.canonical, tt.venueBuy, tt.venueSell, tt.want, got != nil)
		}
	}
}
