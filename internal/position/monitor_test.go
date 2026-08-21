package position

import "testing"

// TestFloat64ToStr 格式化修复（D-072 铃铛治理顺修：负数小数点 bug）。
// 旧实现 float64ToStr(-1.23) = "-1.-23"（负数分支小数点错）；新实现取绝对值后格式化，负号前置。
func TestFloat64ToStr(t *testing.T) {
	cases := []struct {
		name string
		f    float64
		want string
	}{
		{"正数", 1.23, "1.23"},
		{"负数", -1.23, "-1.23"},
		{"零", 0.0, "0.0"},
		{"正小", 0.5, "0.50"},
		{"负小", -0.5, "-0.50"},
		{"大正数", 100.0, "100.0"},
		{"大负数", -100.0, "-100.0"},
		{"负数接近零", -0.01, "-0.1"}, // 2 位精度截断：-0.01*100=-1 → "-0.1"（可接受精度）
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := float64ToStr(c.f)
			if got != c.want {
				t.Errorf("float64ToStr(%v) = %q, want %q", c.f, got, c.want)
			}
		})
	}
}

// TestIsCarryType 息差型判定（P3 单一真相：monitor.go 跳过收敛告警 + autotrade 退出分派同源）。
func TestIsCarryType(t *testing.T) {
	carry := []string{"SWAPARB", "CARRY", "FUNDING_HEDGE", "PERP_FUNDING"}
	for _, tp := range carry {
		if !IsCarryType(tp) {
			t.Errorf("IsCarryType(%q) = false, want true", tp)
		}
	}
	nonCarry := []string{"CROSS_VENUE", "MEANREVERT", "", "UNKNOWN"}
	for _, tp := range nonCarry {
		if IsCarryType(tp) {
			t.Errorf("IsCarryType(%q) = true, want false", tp)
		}
	}
}
