package adapter

import (
	"math"
	"testing"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

func TestNormalizeSwapBpsDaily(t *testing.T) {
	cases := []struct {
		name      string
		mode      SwapMode
		s, price  float64
		point, cs float64
		wantBps   float64
		wantOK    bool
	}{
		{"none 停用发 0", SwapModeNone, 0, 1.1, 0, 0, 0, true},
		{"in-points", SwapModeInPoints, 1.0, 1.1, 0.00001, 0, 0.0909, true},
		{"in-points 缺价", SwapModeInPoints, 1.0, 0, 0.00001, 0, 0, false},
		{"per-lot 货币额", SwapModePerLotCurrency, 2.0, 1.1, 0, 100000, 0.1818, true},
		{"per-lot 缺合同规模", SwapModePerLotCurrency, 2.0, 1.1, 0, 0, 0, false},
		{"年化% 负（付息）", SwapModeAnnualPct, -3.0, 1.1, 0, 100000, -0.8219, true},
		{"未标定跳过", SwapModeUncalibrated, 1.0, 1.1, 0, 100000, 0, false},
	}
	for _, c := range cases {
		got, ok := normalizeSwapBpsDaily(c.mode, c.s, c.price, c.point, c.cs)
		if ok != c.wantOK {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.wantOK)
			continue
		}
		if math.Abs(got-c.wantBps) > 0.001 {
			t.Errorf("%s: bps = %v, want ~%v", c.name, got, c.wantBps)
		}
	}
}

func TestMT5SwapMode(t *testing.T) {
	cases := []struct {
		name      string
		t         mt5pb.SwapType
		long, srt float64
		want      SwapMode
	}{
		{"停用+零 rates=真停用", mt5pb.SwapType_SwapType_SwapNone, 0, 0, SwapModeNone},
		{"停用+非零 rates=矛盾数据", mt5pb.SwapType_SwapType_SwapNone, 1.0, 0, SwapModeUncalibrated},
		{"点数", mt5pb.SwapType_SwapType_InPoints, 1, 1, SwapModeInPoints},
		{"标的货币额 s408（WP1 补齐）", mt5pb.SwapType_SwapType_SymInfo_s408, 1, 1, SwapModePerLotCurrency},
		{"保证金货币", mt5pb.SwapType_SwapType_MarginCurrency, 1, 1, SwapModePerLotCurrency},
		{"入金货币", mt5pb.SwapType_SwapType_Currency, 1, 1, SwapModePerLotCurrency},
		{"现价百分比", mt5pb.SwapType_SwapType_PercCurPrice, 1, 1, SwapModeAnnualPct},
		{"开仓价百分比", mt5pb.SwapType_SwapType_PercOpenPrice, 1, 1, SwapModeAnnualPct},
		{"平仓价点未标定", mt5pb.SwapType_SwapType_PointClosePrice, 1, 1, SwapModeUncalibrated},
		{"买价点未标定", mt5pb.SwapType_SwapType_PointBidPrice, 1, 1, SwapModeUncalibrated},
		{"超界 9（FxPro WTI 实证）未标定", mt5pb.SwapType(9), 1, 1, SwapModeUncalibrated},
		{"超界 100 未标定", mt5pb.SwapType(100), 1, 1, SwapModeUncalibrated},
	}
	for _, c := range cases {
		if got := mt5SwapMode(c.t, c.long, c.srt); got != c.want {
			t.Errorf("%s: mode = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestNormalizeSwapBpsDaily_SymInfo_s408 WP1：mode 2（SymInfo_s408=标的货币额）
// 换算同 PerLotCurrency 口径 S/(price×CS)×1e4。
func TestNormalizeSwapBpsDaily_SymInfo_s408(t *testing.T) {
	// FxPro GOLD 实证样例：S=-3.5（标的货币额/手），price=2000，CS=100
	// bps = -3.5 / (2000 × 100) × 10000 = -0.175
	got, ok := normalizeSwapBpsDaily(SwapModePerLotCurrency, -3.5, 2000, 0, 100)
	if !ok {
		t.Fatal("SymInfo_s408 normalize should succeed (same as PerLotCurrency)")
	}
	if math.Abs(got-(-0.175)) > 0.001 {
		t.Errorf("SymInfo_s408 bps = %v, want ~-0.175", got)
	}
}

// TestSwapUncoveredSummary_WP1 WP1：未覆盖 SwapType 计数日志禁静默。
// 验证：有未覆盖类型时函数输出日志（非空 map → 不 panic、不静默）；
// 无未覆盖类型时零日志（空 map → 不输出）。纯行为验证（日志捕获不在单测范围，
// 此处验证函数可安全调用 + map 操作正确）。
func TestSwapUncoveredSummary_WP1(t *testing.T) {
	// 空 map：不应 panic、不应输出（零日志=全覆盖）
	swapUncoveredSummary("mt5", "fxpro", map[int32]int{})

	// 有未覆盖：7→2 符号、9→1 符号（FxPro WTI 实证）
	swapUncoveredSummary("mt5", "fxpro", map[int32]int{
		7: 2,
		9: 1,
	})

	// 单类型单符号
	swapUncoveredSummary("mt4", "ic", map[int32]int{
		9: 1,
	})
}

func TestMT4SwapMode(t *testing.T) {
	cases := []struct {
		name      string
		t, enable int32
		long, srt float64
		want      SwapMode
	}{
		{"停用+零 rates", 2, 0, 0, 0, SwapModeNone},
		{"停用+非零 rates", 2, 0, 1, 0, SwapModeUncalibrated},
		{"点数", 0, 1, 1, 1, SwapModeInPoints},
		{"基准货币/手", 1, 1, 1, 1, SwapModePerLotCurrency},
		{"保证金货币/手", 3, 1, 1, 1, SwapModePerLotCurrency},
		{"年化%", 2, 1, 1, 1, SwapModeAnnualPct},
		{"未知类型", 9, 1, 1, 1, SwapModeUncalibrated},
	}
	for _, c := range cases {
		if got := mt4SwapMode(c.t, c.enable, c.long, c.srt); got != c.want {
			t.Errorf("%s: mode = %v, want %v", c.name, got, c.want)
		}
	}
}
