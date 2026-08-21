package dashboard

import (
	"math"
	"testing"
)

// TestSwapArbRatio_Default ratio=0.5 生效（保留抗单余地）。
func TestSwapArbRatio_Default(t *testing.T) {
	if got := swapArbRatio(0.5); got != 0.5 {
		t.Errorf("swapArbRatio(0.5)=%v, want 0.5", got)
	}
}

// TestSwapArbRatio_Equity10K_Notional5K ratio=0.5 → equity 10K 折半 5K。
func TestSwapArbRatio_Equity10K_Notional5K(t *testing.T) {
	equity := 10000.0
	ratio := swapArbRatio(0.5)
	notional := equity * ratio
	if math.Abs(notional-5000) > 0.01 {
		t.Errorf("equity 10K × ratio 0.5 = %v, want 5000", notional)
	}
}

// TestSwapArbRatio_One_EquivalentOld ratio=1 等价旧行为（100% 资金使用率）。
func TestSwapArbRatio_One_EquivalentOld(t *testing.T) {
	if got := swapArbRatio(1); got != 1 {
		t.Errorf("swapArbRatio(1)=%v, want 1（等价旧行为）", got)
	}
}

// TestSwapArbRatio_Zero_UnwiredEquivalentOld ratio=0（未接线测试）等价 ratio=1 旧行为。
func TestSwapArbRatio_Zero_UnwiredEquivalentOld(t *testing.T) {
	if got := swapArbRatio(0); got != 1 {
		t.Errorf("swapArbRatio(0)=%v, want 1（未接线等价旧行为）", got)
	}
}

// TestSwapArbRatio_OverOne_Clamped ratio>1 钳到 1（防误配放大）。
func TestSwapArbRatio_OverOne_Clamped(t *testing.T) {
	if got := swapArbRatio(1.5); got != 1 {
		t.Errorf("swapArbRatio(1.5)=%v, want 1（>1 钳到 1 防误配放大）", got)
	}
}

// TestSwapArbRatio_Negative_Clamped 负值钳到 1。
func TestSwapArbRatio_Negative_Clamped(t *testing.T) {
	if got := swapArbRatio(-0.1); got != 1 {
		t.Errorf("swapArbRatio(-0.1)=%v, want 1（负值钳到 1）", got)
	}
}
