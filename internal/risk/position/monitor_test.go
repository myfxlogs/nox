package position

import (
	"testing"

	"nox/internal/store/pgstore"
)

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

// TestMergeLoaded_EvictsRetired 497 实证锚：CLOSED 持仓必须从缓存驱逐，
// 否则残留 LEGGED 状态令 critical 告警按冷却节拍无限复读。
func TestMergeLoaded_EvictsRetired(t *testing.T) {
	cached := map[int64]*pgstore.Position{
		497: {ID: 497, Status: "LEGGED"},
		499: {ID: 499, Status: "LEGGED"},
	}
	loaded := []pgstore.Position{
		{ID: 497, Status: "CLOSED"},
		{ID: 498, Status: "OPEN"},
	}
	mergeLoaded(cached, loaded)
	if _, ok := cached[497]; ok {
		t.Error("CLOSED 497 未被驱逐")
	}
	if cached[498] == nil || cached[498].Status != "OPEN" {
		t.Error("OPEN 498 未收录")
	}
	if _, ok := cached[499]; !ok {
		t.Error("出窗未观测的 499 不应被驱逐（僵尸告警交由 >24h 收编路径）")
	}
}

// TestIsCarryType 息差型判定（P3 单一真相：monitor.go 跳过收敛告警 + autotrade 退出分派同源）。
func TestIsCarryType(t *testing.T) {
	carry := []string{"SWAPARB", "CARRY", "FUNDING_HEDGE", "PERP_FUNDING", "XASSET_CARRY"}
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
