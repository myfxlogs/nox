package exec

import (
	"math"
	"testing"
)

// TestDisasterSL D-146 S1：MT 灾难止损价计算（broker 端 SL，下单即带）。
// 语义：距离=名义的 NOX_MT_DISASTER_SL_PCT（默认 2.0%）。
// 多腿 SL = entry × (1 - pct)（入场价下方）；空腿 SL = entry × (1 + pct)（入场价上方）。
// pct ≤ 0 = 禁用返回 0；entry ≤ 0 = 无效返回 0。
func TestDisasterSL(t *testing.T) {
	t.Run("buy leg SL below entry", func(t *testing.T) {
		// AUDJPY 入场 100.0，2% → SL=98.0（下方）
		sl := disasterSL("BUY", 100.0, 2.0)
		if math.Abs(sl-98.0) > 1e-9 {
			t.Fatalf("BUY SL = %v, want 98.0 (entry 100 × (1-0.02))", sl)
		}
	})
	t.Run("sell leg SL above entry", func(t *testing.T) {
		// AUDJPY 入场 100.0，2% → SL=102.0（上方）
		sl := disasterSL("SELL", 100.0, 2.0)
		if math.Abs(sl-102.0) > 1e-9 {
			t.Fatalf("SELL SL = %v, want 102.0 (entry 100 × (1+0.02))", sl)
		}
	})
	t.Run("AUDJPY 2% numeric anchor", func(t *testing.T) {
		// 数值锚：0.06 手 AUDJPY ≈$4.5K 名义 → $90 风险 ≈ 2.5 个日均波动（0.8%）。
		// AUDJPY 入场 107.50（近期区间），2% → BUY SL=105.35 / SELL SL=109.65。
		// 距离 2.15 个基点价位 = 200 bps，远 > 日均 0.8% → 不会被日内正常波动扫掉。
		buySL := disasterSL("BUY", 107.50, 2.0)
		sellSL := disasterSL("SELL", 107.50, 2.0)
		if math.Abs(buySL-105.35) > 1e-6 {
			t.Fatalf("AUDJPY BUY SL = %v, want 105.35", buySL)
		}
		if math.Abs(sellSL-109.65) > 1e-6 {
			t.Fatalf("AUDJPY SELL SL = %v, want 109.65", sellSL)
		}
		// 验证距离 = 2% × entry
		if math.Abs((107.50-buySL)/107.50*100-2.0) > 1e-9 {
			t.Fatalf("BUY SL distance = %v%%, want 2.0", (107.50-buySL)/107.50*100)
		}
		if math.Abs((sellSL-107.50)/107.50*100-2.0) > 1e-9 {
			t.Fatalf("SELL SL distance = %v%%, want 2.0", (sellSL-107.50)/107.50*100)
		}
	})
	t.Run("disabled when pct <= 0", func(t *testing.T) {
		if sl := disasterSL("BUY", 100.0, 0); sl != 0 {
			t.Fatalf("pct=0 SL = %v, want 0 (disabled)", sl)
		}
		if sl := disasterSL("SELL", 100.0, -1.0); sl != 0 {
			t.Fatalf("pct<0 SL = %v, want 0 (disabled)", sl)
		}
	})
	t.Run("zero entry returns 0", func(t *testing.T) {
		if sl := disasterSL("BUY", 0, 2.0); sl != 0 {
			t.Fatalf("entry=0 SL = %v, want 0", sl)
		}
	})
	t.Run("MT4 same formula (同构)", func(t *testing.T) {
		// MT4 同构——同一 disasterSL 函数，MT4 executor 同样调用。
		// EURUSD 入场 1.1000，2% → BUY SL=1.0780 / SELL SL=1.1220。
		buySL := disasterSL("BUY", 1.1000, 2.0)
		sellSL := disasterSL("SELL", 1.1000, 2.0)
		if math.Abs(buySL-1.078) > 1e-9 {
			t.Fatalf("EURUSD BUY SL = %v, want 1.078 (MT4 同构)", buySL)
		}
		if math.Abs(sellSL-1.122) > 1e-9 {
			t.Fatalf("EURUSD SELL SL = %v, want 1.122 (MT4 同构)", sellSL)
		}
	})
}
