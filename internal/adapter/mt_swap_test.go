package adapter

import (
	"math"
	"testing"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

func TestNormalizeSwapBpsDaily(t *testing.T) {
	cases := []struct {
		name         string
		mode         SwapMode
		s, price     float64
		point, cs    float64
		wantBps      float64
		wantOK       bool
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
		name       string
		t          mt5pb.SwapType
		long, srt  float64
		want       SwapMode
	}{
		{"停用+零 rates=真停用", mt5pb.SwapType_SwapType_SwapNone, 0, 0, SwapModeNone},
		{"停用+非零 rates=矛盾数据", mt5pb.SwapType_SwapType_SwapNone, 1.0, 0, SwapModeUncalibrated},
		{"点数", mt5pb.SwapType_SwapType_InPoints, 1, 1, SwapModeInPoints},
		{"保证金货币", mt5pb.SwapType_SwapType_MarginCurrency, 1, 1, SwapModePerLotCurrency},
		{"入金货币", mt5pb.SwapType_SwapType_Currency, 1, 1, SwapModePerLotCurrency},
		{"现价百分比", mt5pb.SwapType_SwapType_PercCurPrice, 1, 1, SwapModeAnnualPct},
		{"开仓价百分比", mt5pb.SwapType_SwapType_PercOpenPrice, 1, 1, SwapModeAnnualPct},
		{"s408 未标定", mt5pb.SwapType_SwapType_SymInfo_s408, 1, 1, SwapModeUncalibrated},
		{"平仓价点未标定", mt5pb.SwapType_SwapType_PointClosePrice, 1, 1, SwapModeUncalibrated},
		{"买价点未标定", mt5pb.SwapType_SwapType_PointBidPrice, 1, 1, SwapModeUncalibrated},
	}
	for _, c := range cases {
		if got := mt5SwapMode(c.t, c.long, c.srt); got != c.want {
			t.Errorf("%s: mode = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMT4SwapMode(t *testing.T) {
	cases := []struct {
		name        string
		t, enable   int32
		long, srt   float64
		want        SwapMode
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
