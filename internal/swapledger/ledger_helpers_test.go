package swapledger

import "testing"

// TestCashflowKind D-314 测试锚 1-4：per-leg kind 判定 + 向后兼容（替代旧单参数 TestCashflowKind）。
// XASSET_CARRY 混合 venue：CEX :perp 腿=funding、MT 腿=swap；其他类型按 posType 不变。
func TestCashflowKind(t *testing.T) {
	tests := []struct {
		posType, venue, want string
	}{
		{"XASSET_CARRY", "gate:perp", "funding"},  // 锚1：perp 腿=funding
		{"XASSET_CARRY", "277842155:mt5", "swap"}, // 锚2：MT 腿=swap
		{"SWAPARB", "277842155:mt5", "swap"},      // 锚3：向后兼容
		{"FUNDING_HEDGE", "gate:perp", "funding"}, // 锚4：向后兼容
		{"CARRY", "52993526:mt5", "swap"},
		{"PERP_FUNDING", "gate:perp", "funding"},
	}
	for _, tt := range tests {
		if got := cashflowKind(tt.posType, tt.venue); got != tt.want {
			t.Errorf("cashflowKind(%q, %q) want %q, got %q", tt.posType, tt.venue, tt.want, got)
		}
	}
}

// TestIsFundingLeg D-314 测试锚 5-8：isFundingLeg per-leg 判定。
func TestIsFundingLeg(t *testing.T) {
	tests := []struct {
		posType, venue string
		want           bool
	}{
		{"XASSET_CARRY", "gate:perp", true},      // 锚5
		{"XASSET_CARRY", "277842155:mt5", false}, // 锚6
		{"XASSET_CARRY", "gate:spot", false},     // 锚7：spot 不走 funding
		{"SWAPARB", "277842155:mt5", false},      // 锚8：向后兼容
		{"FUNDING_HEDGE", "52993526:mt5", true},  // 向后兼容：funding 类型不看 venue
		{"PERP_FUNDING", "277842155:mt5", true},  // 向后兼容：funding 类型不看 venue
	}
	for _, tt := range tests {
		if got := isFundingLeg(tt.posType, tt.venue); got != tt.want {
			t.Errorf("isFundingLeg(%q, %q) want %v, got %v", tt.posType, tt.venue, tt.want, got)
		}
	}
}
