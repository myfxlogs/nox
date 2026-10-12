package fact

import "testing"

// TestCalcModeToProductGroup D-173：MT5 CalcMode → product_group 映射（先红后绿）。
func TestCalcModeToProductGroup(t *testing.T) {
	cases := []struct {
		calcMode int32
		want     string
	}{
		{0, "fx"},
		{1, "fut"},
		{2, "cfd_spot"},
		{3, "cfd_index"},
		{4, "cfd_leverage"},
		{33, "fut"},
		{34, "fut"},
		{99, ""}, // 未知 CalcMode → 空（配对时放行）
	}
	for _, c := range cases {
		if got := CalcModeToProductGroup(c.calcMode); got != c.want {
			t.Errorf("CalcModeToProductGroup(%d) = %q, want %q", c.calcMode, got, c.want)
		}
	}
}
