package autotrade

import "testing"

// TestSameProductClass D-558：产品维度配对校验纯函数（等价键=canonical 产品类）。
// 同 canonical 异组放行语义=两腿解出同类（XAUUSD@fx × XAUUSD@cfd_spot 都=metal）；
// 类异=真异品拒（BTCUSDT×WTI：crypto×energy）；未归类 fail-closed 拒（禁粗到全放行）。
func TestSameProductClass(t *testing.T) {
	cases := []struct {
		name       string
		longClass  string
		shortClass string
		want       bool
	}{
		{"同类放行（同品异组：fx×cfd_spot 两腿皆 metal）", "metal", "metal", true},
		{"同类放行（crypto）", "crypto", "crypto", true},
		{"类异拒（真异品：BTCUSDT×WTI）", "crypto", "energy", false},
		{"类异拒（真外汇×金属）", "fx", "metal", false},
		{"长腿未归类拒", "", "fx", false},
		{"短腿未归类拒", "fx", "", false},
		{"双未归类拒", "", "", false},
	}
	for _, c := range cases {
		if got := sameProductClass(c.longClass, c.shortClass); got != c.want {
			t.Errorf("%s: sameProductClass(%q, %q) = %v, want %v", c.name, c.longClass, c.shortClass, got, c.want)
		}
	}
}
