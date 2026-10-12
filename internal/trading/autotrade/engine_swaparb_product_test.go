package autotrade

import "testing"

// TestSameProductGroup D-173：产品维度配对校验纯函数（先红后绿）。
func TestSameProductGroup(t *testing.T) {
	cases := []struct {
		name    string
		longPG  string
		shortPG string
		want    bool
	}{
		{"空×空", "", "", true},
		{"空×非空", "", "fx", true},
		{"非空×空", "fx", "", true},
		{"相同", "cfd_spot", "cfd_spot", true},
		{"不同", "cfd_spot", "cfd_leverage", false}, // 470 根因：CFD 现货 × CFDLeverage
	}
	for _, c := range cases {
		if got := sameProductGroup(c.longPG, c.shortPG); got != c.want {
			t.Errorf("%s: sameProductGroup(%q, %q) = %v, want %v", c.name, c.longPG, c.shortPG, got, c.want)
		}
	}
}
