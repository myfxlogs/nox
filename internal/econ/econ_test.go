package econ

import (
	"math"
	"testing"
)

// TestSnapshotDerived 公式矩阵：BreakevenDays=(entry+exit)/carry；NetIfClosedNow=accrued+capture−entry−exit。
func TestSnapshotDerived(t *testing.T) {
	cases := []struct {
		name           string
		entry, exit    float64
		carry, capture float64
		accrued        float64
		wantBE         float64
		wantNet        float64
	}{
		{"#505 形态: 4.16/2.37≈1.75d, net=−4.16", 2.08, 2.08, 2.37, 0, 0, 4.16 / 2.37, -4.16},
		{"收敛+日积: net=20.8+2−4.16=18.64", 2.08, 2.08, 2.37, 20.8, 2.0, 4.16 / 2.37, 18.64},
		{"carry=0 → BE=+Inf", 2.0, 2.0, 0, 10, 5, math.Inf(1), 11.0},
		{"carry<0 → BE=+Inf", 2.0, 2.0, -1.0, 10, 5, math.Inf(1), 11.0},
	}
	for _, tc := range cases {
		s := Snapshot{EntryCostUSD: tc.entry, ExitCostEstUSD: tc.exit,
			CarryPerDayUSD: tc.carry, CaptureUSD: tc.capture, AccruedUSD: tc.accrued}
		if be := s.BreakevenDays(); math.Abs(be-tc.wantBE) > 1e-9 && !math.IsInf(be, 1) {
			t.Errorf("%s: BreakevenDays=%v want %v", tc.name, be, tc.wantBE)
		}
		if net := s.NetIfClosedNow(); math.Abs(net-tc.wantNet) > 1e-9 {
			t.Errorf("%s: NetIfClosedNow=%v want %v", tc.name, net, tc.wantNet)
		}
	}
}

// TestEntryOK 入场闸：carry≤0 永拒；breakeven≤cap 才放行（20.8%/yr 收 / 11.6%/yr 拒锚）。
func TestEntryOK(t *testing.T) {
	// N=$4160 腿：carry 20.8%/yr=$2.37/d → BE=(4.16)/2.37≈1.75 ≤3 收；11.6%/yr=$1.32/d → BE≈3.15 >3 拒。
	good := Snapshot{EntryCostUSD: 2.08, ExitCostEstUSD: 2.08, CarryPerDayUSD: 4160 * 0.208 / 365}
	bad := Snapshot{EntryCostUSD: 2.08, ExitCostEstUSD: 2.08, CarryPerDayUSD: 4160 * 0.116 / 365}
	if !good.EntryOK(3.0) {
		t.Errorf("20.8%%/yr（$2.37/d）应过 3d 闸, BE=%.2f", good.BreakevenDays())
	}
	if bad.EntryOK(3.0) {
		t.Errorf("11.6%%/yr（$1.32/d）应被 3d 闸拒, BE=%.2f", bad.BreakevenDays())
	}
	zero := Snapshot{EntryCostUSD: 1, CarryPerDayUSD: 0}
	if zero.EntryOK(3.0) {
		t.Error("carry=0 永拒")
	}
	neg := Snapshot{EntryCostUSD: 1, CarryPerDayUSD: -0.5}
	if neg.EntryOK(3.0) {
		t.Error("carry<0 永拒")
	}
}

// TestHarvestOK 收割闸：NetIfClosedNow<0 永不收割；≥0 且 ≥CarryPerDay×horizon 才收割。
func TestHarvestOK(t *testing.T) {
	neg := Snapshot{EntryCostUSD: 2.08, ExitCostEstUSD: 2.08, CarryPerDayUSD: 2.37, CaptureUSD: 1.0} // net<0
	if neg.HarvestOK(7.0) {
		t.Error("net<0 永不收割")
	}
	// net=+2.0 ≥0 但 < carry×7=16.6 → 不收（继续养）。
	short := Snapshot{EntryCostUSD: 2.08, ExitCostEstUSD: 2.08, CarryPerDayUSD: 2.37, CaptureUSD: 6.16}
	if short.HarvestOK(7.0) {
		t.Errorf("net=%.2f < carry×7=%.2f 不应收割", short.NetIfClosedNow(), 2.37*7)
	}
	// net=17.0 ≥16.6 → 收。
	full := Snapshot{EntryCostUSD: 2.08, ExitCostEstUSD: 2.08, CarryPerDayUSD: 2.37, CaptureUSD: 20.8, AccruedUSD: 0.5}
	if !full.HarvestOK(7.0) {
		t.Errorf("net=%.2f ≥ carry×7 应收割", full.NetIfClosedNow())
	}
	// carry≤0 但有正 net：收割不依赖 carry 符号（净额已满 horizo​​n 判据时×0 天然不满足——
	// carry≤0 → carry×7≤0 → net≥0 即满足第二条？显式钉语义：carry≤0 时 horizon 判据=net≥0）。
	dead := Snapshot{EntryCostUSD: 1, ExitCostEstUSD: 1, CarryPerDayUSD: 0, CaptureUSD: 5, AccruedUSD: 5}
	if !dead.HarvestOK(7.0) {
		t.Error("carry=0 且 net=+8≥0：HorizonDays×0=0 ≤net → 收割放行（收敛利润已足）")
	}
}
