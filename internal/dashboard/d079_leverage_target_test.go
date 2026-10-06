package dashboard

// d079_leverage_target_test.go —— D-079 锚④：legMarginNeedPerp 策略值对齐。
// venue 实读 5× + target=10 → frac=0.1（=1/target=引擎开仓前置 ensure 后真实状态）、
// note 透出「策略值」；target=0 → 不换算、原样透出实读值（旧行为）。

import (
	"context"
	"strings"
	"testing"
)

func TestD079_PerpLegMarginNeed_StrategyTarget(t *testing.T) {
	s := d563VerdictSvc(t, 5000, 10000)
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) { return 5, nil }

	// target=10，venue 现值 5×≠目标 → frac=1/10、note 载明「杠杆 5×→开仓前置设为 10×（策略值）」
	s.SetPerpLeverageTarget(10)
	frac, note, known := s.legMarginNeedPerp(context.Background(), "gate:perp", "BTC_USDT", true)
	if !known || frac != 0.1 {
		t.Fatalf("策略目标应 frac=0.1 known=true, got frac=%v known=%v note=%q", frac, known, note)
	}
	if !strings.Contains(note, "策略值") || !strings.Contains(note, "5×→开仓前置设为 10×") {
		t.Fatalf("note 应透出策略值变换: %q", note)
	}

	// target=0 → 不换算，原样透出实读 1/5=0.2
	s.SetPerpLeverageTarget(0)
	frac2, note2, known2 := s.legMarginNeedPerp(context.Background(), "gate:perp", "BTC_USDT", false)
	if !known2 || frac2 != 0.2 || !strings.Contains(note2, "实读") {
		t.Fatalf("target=0 应原样透出实读值, got frac=%v note=%q", frac2, note2)
	}

	// target=5 与实读同值（|Δ|<0.5）→ 也按实读路径透出
	s.SetPerpLeverageTarget(5)
	frac3, _, _ := s.legMarginNeedPerp(context.Background(), "gate:perp", "BTC_USDT", true)
	if frac3 != 0.2 {
		t.Fatalf("实读=目标时应按实读 frac=0.2, got %v", frac3)
	}
}
