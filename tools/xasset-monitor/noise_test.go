package main

// S1/T4：noise_bps=24h 摆幅半径 (max−min)/2；样本 <12 → NULL（<1h 数据不可信）。
import "testing"

func TestD335_ComputeNoiseBps(t *testing.T) {
	// 设计 T4 字面序列 [10,15,20,12] 铺满 12 样本门槛 → (20−10)/2=5
	vals := []float64{}
	for i := 0; i < 3; i++ {
		vals = append(vals, []float64{10, 15, 20, 12}...)
	}
	got := computeNoiseBps(vals)
	if got == nil || *got != 5 {
		t.Errorf("noise want 5, got %v", got)
	}
	// 样本 <12 → NULL
	if got := computeNoiseBps([]float64{10, 15, 20, 12}); got != nil {
		t.Errorf("<12 samples want nil, got %v", *got)
	}
}
