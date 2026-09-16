package pgstore

import "testing"

// TestPositionLeg_EffectiveQty D-311 R6：实际持仓量=成交口径（时代稳定）。
// 新行 qty=LegSpec 请求量（CEX=合约数）、filled_qty=成交基础币 → 取 filled；
// 旧行 filled_qty NULL → 回退 qty（旧时代 qty 本身即成交量）；filled≤0 → 回退 qty。
func TestPositionLeg_EffectiveQty(t *testing.T) {
	filled := 10.12
	zero := 0.0
	neg := -1.0
	cases := []struct {
		name string
		leg  PositionLeg
		want float64
	}{
		{"new row: filled wins over contract-count qty", PositionLeg{Qty: 1012, FilledQty: &filled}, 10.12},
		{"old row: NULL filled falls back to qty", PositionLeg{Qty: 2950}, 2950},
		{"zero filled falls back to qty", PositionLeg{Qty: 0.5, FilledQty: &zero}, 0.5},
		{"negative filled falls back to qty", PositionLeg{Qty: 0.5, FilledQty: &neg}, 0.5},
	}
	for _, c := range cases {
		if got := c.leg.EffectiveQty(); got != c.want {
			t.Errorf("%s: EffectiveQty=%v, want %v", c.name, got, c.want)
		}
	}
}
