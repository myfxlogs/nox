package decimalutil

import (
	"testing"

	"github.com/shopspring/decimal"
)

// TestFromFloat64_Precision 1.05 精度回归锚（D-077 修复⑩ G5）。
// decimal.NewFromFloat(1.05) 会得到 1.0500000000000000444...（二进制不精确）；
// FromFloat64 经 strconv 字符串中介 → 精确 1.05。
func TestFromFloat64_Precision(t *testing.T) {
	d, err := FromFloat64(1.05)
	if err != nil {
		t.Fatalf("FromFloat64(1.05): %v", err)
	}
	want := "1.05"
	got := d.String()
	if got != want {
		t.Errorf("FromFloat64(1.05) want %q, got %q (decimal.NewFromFloat would give imprecise)", want, got)
	}
}

// TestFromFloat64_RoundTrip 整数/常见小数往返精确。
func TestFromFloat64_RoundTrip(t *testing.T) {
	tests := []struct {
		f    float64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{100, "100"},
		{0.1, "0.1"},
		{0.01, "0.01"},
		{123.456, "123.456"},
		{-1.05, "-1.05"},
		{0.0001, "0.0001"},
	}
	for _, tt := range tests {
		d, err := FromFloat64(tt.f)
		if err != nil {
			t.Fatalf("FromFloat64(%v): %v", tt.f, err)
		}
		if d.String() != tt.want {
			t.Errorf("FromFloat64(%v) want %q, got %q", tt.f, tt.want, d.String())
		}
	}
}

// TestFromFloat64_Arithmetic 累加无精度漂移（swapledger 接入场景）。
func TestFromFloat64_Arithmetic(t *testing.T) {
	// 模拟 swap 记账累加：0.1 + 0.2 = 0.3（float64 直接加 = 0.30000000000000004）
	d1, _ := FromFloat64(0.1)
	d2, _ := FromFloat64(0.2)
	sum := d1.Add(d2)
	want := "0.3"
	if sum.String() != want {
		t.Errorf("0.1+0.2 want %q, got %q", want, sum.String())
	}
}

// TestMustFromFloat64_NaN panic on NaN（仅测试异常路径）。
func TestMustFromFloat64_NaN(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustFromFloat64(NaN) should panic")
		}
	}()
	var z float64
	MustFromFloat64(z / z) // NaN
	_ = decimal.NewFromInt(0)
}

// TestFromFloat64_Multiplication 乘法无精度漂移（swapledger notional×rate 场景）。
func TestFromFloat64_Multiplication(t *testing.T) {
	// 50000 × 0.0001 = 5.0（float64 直接乘可能有漂移）
	notional, _ := FromFloat64(50000)
	rate, _ := FromFloat64(0.0001)
	product := notional.Mul(rate)
	want := "5"
	if product.String() != want {
		t.Errorf("50000×0.0001 want %q, got %q", want, product.String())
	}
}
