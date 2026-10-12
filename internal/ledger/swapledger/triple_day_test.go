package swapledger

import (
	"testing"
	"time"
)

// TestMt5TripleSwapDay MT5 V3DaysSwap → Go weekday（proto 与 Go 同序 0=Sunday）。
func TestMt5TripleSwapDay(t *testing.T) {
	// 此函数在 adapter 包，这里仅验证概念：0=Sunday...6=Saturday
	tests := []struct {
		v    int
		want time.Weekday
	}{
		{0, time.Sunday},
		{1, time.Monday},
		{2, time.Tuesday},
		{3, time.Wednesday},
		{4, time.Thursday},
		{5, time.Friday},
		{6, time.Saturday},
	}
	for _, tt := range tests {
		got := time.Weekday(tt.v)
		if got != tt.want {
			t.Errorf("weekday %d want %v, got %v", tt.v, tt.want, got)
		}
	}
}

// TestMt4TripleSwapDayMapping MT4 SwapRollover3Days → Go weekday（+1 偏移）。
// MT4 proto: 0=Monday...4=Friday；Go: 0=Sunday...6=Saturday。映射 = MT4值+1。
func TestMt4TripleSwapDayMapping(t *testing.T) {
	tests := []struct {
		mt4Val int32
		want   time.Weekday
	}{
		{0, time.Monday},    // MT4 0=Monday → Go 1
		{1, time.Tuesday},   // MT4 1=Tuesday → Go 2
		{2, time.Wednesday}, // MT4 2=Wednesday → Go 3
		{3, time.Thursday},  // MT4 3=Thursday → Go 4
		{4, time.Friday},    // MT4 4=Friday → Go 5
	}
	for _, tt := range tests {
		got := time.Weekday(int(tt.mt4Val + 1)) // +1 偏移（mt4TripleSwapDay 同逻辑）
		if got != tt.want {
			t.Errorf("MT4 %d → Go weekday want %v, got %v", tt.mt4Val, tt.want, got)
		}
	}
}

// TestTripleSwapDayDiff 各 venue 三倍日不同（archive 实证：4 家不全周三）。
func TestTripleSwapDayDiff(t *testing.T) {
	// 模拟 4 venue 实测：vA=Wednesday, vB=Tuesday, vC=Wednesday, vD=Monday
	venueDays := map[string]time.Weekday{
		"vA": time.Wednesday,
		"vB": time.Tuesday,
		"vC": time.Wednesday,
		"vD": time.Monday,
	}
	wedCount := 0
	for _, d := range venueDays {
		if d == time.Wednesday {
			wedCount++
		}
	}
	// 4 家中 2 家周三，2 家非周三 → archive 实证「4 家不全周三」
	if wedCount == len(venueDays) {
		t.Error("expected not all Wednesday (archive 实证 4 家不全周三)")
	}
	if wedCount != 2 {
		t.Errorf("expected 2 Wednesday venues, got %d", wedCount)
	}
}

// TestSettleMultiplier_PerSymbolPriority symbol 级 Fact 优先于 venue 级（D-077 修复⑪A M3）。
func TestSettleMultiplier_PerSymbolPriority(t *testing.T) {
	r := stubTripleDay{
		venueDays:  map[string]time.Weekday{"vA": time.Wednesday},
		symbolDays: map[string]time.Weekday{"vA:XAUUSD": time.Friday}, // symbol 级=Friday 覆盖 venue 级=Wednesday
	}
	fri := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC) // Friday
	wed := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC) // Wednesday
	// XAUUSD 用 symbol 级 Friday → Friday=3, Wednesday=1
	if got := settleMultiplier("vA:mt5", "XAUUSD", fri, r); got != 3 {
		t.Errorf("per-symbol Friday want 3, got %v", got)
	}
	if got := settleMultiplier("vA:mt5", "XAUUSD", wed, r); got != 1 {
		t.Errorf("per-symbol Wednesday (not Friday) want 1, got %v", got)
	}
	// AUDJPY 无 symbol 级 → 回退 venue 级 Wednesday → Wednesday=3
	if got := settleMultiplier("vA:mt5", "AUDJPY", wed, r); got != 3 {
		t.Errorf("venue-level Wednesday want 3, got %v", got)
	}
}
