// freemargin_risk_test.go —— D-006 S2：freeMarginRiskLevel 双因子判级表驱动锚。
// critical=fm≤Σnotional/20；warn=fm<max(0.2×equity,1.5×need)；否则不报。
package main

import "testing"

func TestD006_FreeMarginRiskLevel(t *testing.T) {
	cases := []struct {
		name             string
		fm, equity, need float64
		want             string
	}{
		// 收紧缺口：fm ≤ need → critical（含边界相等）。
		{"临界相等critical", 100, 1000, 100, "critical"},
		{"缺口critical", 50, 1000, 100, "critical"},
		// warn 双因子：fm > need 但 < max(0.2eq, 1.5need)。
		{"1.5need下warn", 140, 1000, 100, "warn"}, // 140<150（1.5×100）且<200（0.2×1000）
		{"0.2eq下warn", 190, 1000, 100, "warn"},   // 190<200（0.2eq 主导）且>1.5need
		// 健康水位：≥ 双因子阈。
		{"阈值上不报", 250, 1000, 100, ""},
		{"eq主导阈值上", 300, 800, 100, ""}, // max(160,150)=160；300>160
		// 空仓（need=0）：critical 永不触发（fm≤0 才触发），warn 只看 eq 因子。
		{"空仓eq低warn", 15, 100, 0, "warn"}, // 15<20（0.2×100）
		{"空仓eq健康不报", 25, 100, 0, ""},
		// 无限杠杆户实拍形态：need 大 fm 小 → critical 咬紧。
		{"实拍形态critical", 500, 5000, 1000, "critical"},
	}
	for _, c := range cases {
		if got := freeMarginRiskLevel(c.fm, c.equity, c.need); got != c.want {
			t.Fatalf("%s: fm=%.0f eq=%.0f need=%.0f want %q got %q", c.name, c.fm, c.equity, c.need, c.want, got)
		}
	}
}
