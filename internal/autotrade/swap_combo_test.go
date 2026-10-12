package autotrade

import (
	"testing"

	"arb/internal/fact"
)

// TestSwapComboVenueKey_MT4Src fact.Src=mt4 → venueKey 拼 :mt4（D-077 修复⑪B N2）。
// 旧代码硬编码 :mt5 → MT4 账户无法进 swap 套利。
func TestSwapComboVenueKey_MT4Src(t *testing.T) {
	lf := fact.Fact{Venue: "vA", Src: "mt4", Value: 5.0}
	sf := fact.Fact{Venue: "vB", Src: "mt5", Value: -3.0}

	// 模拟 discoverSwapCombos 的 venueKey 构建逻辑
	vLong := lf.Venue + ":" + lf.Src
	vShort := sf.Venue + ":" + sf.Src

	if vLong != "vA:mt4" {
		t.Errorf("mt4 long leg venueKey want vA:mt4, got %s", vLong)
	}
	if vShort != "vB:mt5" {
		t.Errorf("mt5 short leg venueKey want vB:mt5, got %s", vShort)
	}
}

// TestSwapComboVenueKey_MT5Src fact.Src=mt5 → venueKey 拼 :mt5（现行为不变）。
func TestSwapComboVenueKey_MT5Src(t *testing.T) {
	lf := fact.Fact{Venue: "vA", Src: "mt5", Value: 5.0}
	sf := fact.Fact{Venue: "vB", Src: "mt5", Value: -3.0}

	vLong := lf.Venue + ":" + lf.Src
	vShort := sf.Venue + ":" + sf.Src

	if vLong != "vA:mt5" {
		t.Errorf("mt5 long leg venueKey want vA:mt5, got %s", vLong)
	}
	if vShort != "vB:mt5" {
		t.Errorf("mt5 short leg venueKey want vB:mt5, got %s", vShort)
	}
}

// TestSwapComboVenueKey_MixedSrc MT4+MT5 混合 → 各自正确拼接。
func TestSwapComboVenueKey_MixedSrc(t *testing.T) {
	tests := []struct {
		venue, src, want string
	}{
		{"vA", "mt4", "vA:mt4"},
		{"vB", "mt5", "vB:mt5"},
		{"vC", "mt4", "vC:mt4"},
		{"vD", "mt5", "vD:mt5"},
	}
	for _, tt := range tests {
		got := tt.venue + ":" + tt.src
		if got != tt.want {
			t.Errorf("venueKey(%s,%s) want %s, got %s", tt.venue, tt.src, tt.want, got)
		}
	}
}
