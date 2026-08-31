package autotrade

import (
	"testing"
)

// TestGrossSplitFloor_D-179 S4：折分 qty 向下规整到 0.01 步长。
func TestGrossSplitFloor(t *testing.T) {
	tests := []struct {
		name    string
		qty     float64
		wantQty float64
		reject  bool
	}{
		{"0.02 → 0.02（整步长）", 0.02, 0.02, false},
		{"0.015 → 0.01（向下规整）", 0.015, 0.01, false},
		{"0.019 → 0.01（向下规整）", 0.019, 0.01, false},
		{"0.0109 → 0.01（posID=470 场景）", 0.0109, 0.01, false},
		{"0.009 → reject（<0.01）", 0.009, 0, true},
		{"0.005 → reject（<0.01）", 0.005, 0, true},
		{"0.05 → 0.05", 0.05, 0.05, false},
		{"0.025 → 0.02", 0.025, 0.02, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reject := grossSplitFloor(tt.qty)
			if reject != tt.reject {
				t.Errorf("grossSplitFloor(%.4f) reject=%v, want %v", tt.qty, reject, tt.reject)
			}
			if !reject && got != tt.wantQty {
				t.Errorf("grossSplitFloor(%.4f) = %.4f, want %.4f", tt.qty, got, tt.wantQty)
			}
		})
	}
}

// TestGrossSplitFloor_PosID470 D-179 S4 回归：posID=470 场景——折分 qty 0.0109 地板。
// posID=470 实证：gate 折分 qty 0.0223→0.0109（单腿 $2008→$983），MT 服务端规整到 0.02 成交。
// S4 修复后：0.0109 向下规整到 0.01（≥0.01 不拒开），但若折分到 0.009 则拒开。
func TestGrossSplitFloor_PosID470(t *testing.T) {
	// 0.0109 → 0.01（≥0.01，不拒开）
	got, reject := grossSplitFloor(0.0109)
	if reject {
		t.Error("0.0109 should not reject (floors to 0.01)")
	}
	if got != 0.01 {
		t.Errorf("0.0109 should floor to 0.01, got %.4f", got)
	}
	// 若折分到 0.009 → 拒开（S4 防御）
	_, reject = grossSplitFloor(0.009)
	if !reject {
		t.Error("0.009 should reject (<0.01 min lot floor)")
	}
}

// TestOverfillDetection D-179 S4：overfill 检测逻辑——filled > requested×1.001 → 告警。
// 验证判断条件（mt5_executor.go:70 的比较逻辑，不含 gRPC 调用）。
func TestOverfillDetection(t *testing.T) {
	tests := []struct {
		name      string
		requested float64
		filled    float64
		overfill  bool
	}{
		{"exact match", 0.01, 0.01, false},
		{"within tolerance", 0.01, 0.01001, false},
		{"overfill 0.0109→0.02 (posID=470)", 0.0109, 0.02, true},
		{"overfill 2x", 0.01, 0.02, true},
		{"underfill", 0.02, 0.01, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.filled > tt.requested*1.001
			if got != tt.overfill {
				t.Errorf("overfill(req=%.4f, filled=%.4f) = %v, want %v", tt.requested, tt.filled, got, tt.overfill)
			}
		})
	}
}
