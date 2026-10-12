// d594_notional_test.go D-594 T 组：canonical=USD/lb 口径下的名义额可比性（A2）。
package usdnotional

import (
	"math"
	"testing"
)

// TestD594CopperNotionalComparable canonical 价=USD/lb、units/lot=CS_raw×qty_scale：
// 1 吨手（2204.62 lb×$6.58≈$14.5k）与 2000 lb 手（2000×$6.67≈$13.3k）同量级（A2）。
func TestD594CopperNotionalComparable(t *testing.T) {
	// 1279967：CS=1 吨、qty_scale=2204.62 → canonical units/lot=2204.62 lb；canonical 价 $6.58/lb。
	ton, ok := PerLot("XCUUSD", 6.58, 1*2204.62, nil)
	if !ok {
		t.Fatal("XCUUSD 应入 USD 计价白名单（canonical 后场景）")
	}
	// 61585565：CS=2000 lb、qty_scale=1（lb→lb 恒等）→ units/lot=2000 lb；价 $6.67/lb。
	lb, ok := PerLot("XCUUSD", 6.67, 2000, nil)
	if !ok {
		t.Fatal("lb venue perlot")
	}
	// 25 吨手（591959217）：55000+ lb × $6.6 ≈ $364k（A2 容许 25× 因子可释）。
	ton25, ok := PerLot("XCUUSD", 6.6, 25*2204.62, nil)
	if !ok {
		t.Fatal("25ton venue perlot")
	}
	if ratio := ton / lb; ratio < 0.8 || ratio > 1.4 {
		t.Errorf("1 吨手 $%.0f vs 2000lb 手 $%.0f 不同量级（ratio %.3f）", ton, lb, ratio)
	}
	if ton25 <= ton*20 || ton25 >= ton*30 {
		t.Errorf("25 吨手 $%.0f 应为 1 吨手 $%.0f 的 ~25 倍", ton25, ton)
	}
	if math.Abs(ton-14500) > 200 {
		t.Errorf("1 吨手名义 $%.0f 应 ≈ $14500（14500$/ton 真值）", ton)
	}
}
