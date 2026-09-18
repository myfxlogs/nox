package main

// D-341 T1（harvest_floor 计算+样本守卫）/T5（ticks×klines 对齐纯函数）。
import (
	"testing"
	"time"
)

func TestD341_ComputeHarvestFloor(t *testing.T) {
	// 序列 0..47（48 样本）铺满门槛：q25≈11.75；entry=20 → floor=min(11.75, 17)=11.75
	vals := make([]float64, 48)
	for i := range vals {
		vals[i] = float64(i)
	}
	got := computeHarvestFloor(vals, 20)
	if got == nil {
		t.Fatal("48 样本应出 floor")
	}
	want := 0.25*47 + (0.25*47-11)*(1) // q25 线性插值=11.75
	want = 0.25 * 47                   // lo=11 hi=12 插值=11.75
	if *got < want-0.5 || *got > want+0.5 {
		t.Errorf("q25 want ≈%.2f, got %.4f", want, *got)
	}
	// MIN_CAPTURE 下限：entry=10 < q25+3 → floor=entry−3=7
	got2 := computeHarvestFloor(vals, 10)
	if got2 == nil || *got2 != 7 {
		t.Errorf("MIN_CAPTURE 下限 want 7, got %v", got2)
	}
	// T1 样本 <48 → NULL
	if got := computeHarvestFloor(vals[:47], 20); got != nil {
		t.Errorf("<48 样本 want NULL, got %v", *got)
	}
}

func TestD341_AlignSpread(t *testing.T) {
	base := time.Unix(1788163200, 0)
	ticks := []tickBucket{
		{Bucket: base, Mid: 100},
		{Bucket: base.Add(5 * time.Minute), Mid: 100},
		{Bucket: base.Add(10 * time.Minute), Mid: 100}, // kline 缺此桶 → miss
	}
	klines := []kline{
		{OpenTime: base, Close: 100.5},
		{OpenTime: base.Add(5 * time.Minute), Close: 100.2},
	}
	esps, miss := alignSpread(ticks, klines)
	if len(esps) != 2 || miss != 1 {
		t.Fatalf("esps=%v miss=%d, want 2/1（缺桶跳过计数）", esps, miss)
	}
	// esp=(kline−tick_mid)/mid×1e4：50bps 与 20bps
	if esps[0] < 20-0.01 || esps[0] > 20+0.01 {
		t.Errorf("esp[0] want ≈20, got %.2f", esps[0])
	}
	if esps[1] < 50-0.01 || esps[1] > 50+0.01 {
		t.Errorf("esp[1] want ≈50, got %.2f", esps[1])
	}
}
