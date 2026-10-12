// effective_notional_test.go —— effectiveNotionalUSD 纯函数锚（combo_allow_test 拆出，D-212 拆搬）。
package autotrade

import (
	"testing"

	"nox/internal/trading/exec"
)

func floatPtr(f float64) *float64 { return &f }

// TestEffectiveNotionalUSD D-367 S5 T1/T2：组合级列优先、NULL 回退 env（纯函数）。
func TestEffectiveNotionalUSD(t *testing.T) {
	spec5000 := &exec.XAssetComboAllowSpec{NotionalUSD: floatPtr(5000)}
	if got := effectiveNotionalUSD(3000, spec5000); got != 5000 {
		t.Errorf("T1: spec 列优先 = %v, want 5000", got)
	}
	if got := effectiveNotionalUSD(3000, nil); got != 3000 {
		t.Errorf("T2: spec nil 回退 env = %v, want 3000（行为与今天逐字节同）", got)
	}
	specNil := &exec.XAssetComboAllowSpec{}
	if got := effectiveNotionalUSD(3000, specNil); got != 3000 {
		t.Errorf("T2b: 行存在但列 NULL = %v, want 3000", got)
	}
}

// TestXassetQtyFloor_CapsStillApply D-367 S5 T4：spec 名义额大但 MaxGrossUSD 小 →
// caps 终审仍截断（既有 gross 预闸回归不破——sizing 收编不放开 caps）。
func TestXassetQtyFloor_CapsStillApply(t *testing.T) {
	// NotionalUSD=50000 很大，但 MaxLongQty=0.01×perLot=435.5k → gross=8710 > MaxGrossUSD 5000 → 截断
	spec := &exec.XAssetComboAllowSpec{
		MaxLongQty: 0.01, MaxGrossUSD: 5000, NotionalUSD: floatPtr(50000),
	}
	qty, nn, ok := xassetQtyFloor(spec, true, 435500)
	if ok {
		t.Errorf("MaxGrossUSD 预闸应截断, got qty=%v notional=%v", qty, nn)
	}
	// 对照：MaxGrossUSD 放开 → floor 生效（既有逻辑回归）
	spec.MaxGrossUSD = 100000
	qty, nn, ok = xassetQtyFloor(spec, true, 435500)
	if !ok || qty != 0.01 || nn != 4355 {
		t.Errorf("floor = %v/%v/%v, want 0.01/4355/true", qty, nn, ok)
	}
}
