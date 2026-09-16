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
		{"FxPro 扩展 9=InPoints（D-092 标定）", mt5pb.SwapType(9), 5.5, -51.55, SwapModeInPoints},
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

// TestMT5SwapMode9_InPointsNormalize D-092：mode 9（FxPro 扩展）标定为 InPoints 后，
// 用 S1 真值手算断言归一化 bps。证据见 docs/油类SwapType9标定-施工提示词.md S1 表。
// WTI: L=+5.50 S=-51.55 point=0.001 CS=1000 → point×CS=$1/点/手
//
//	WTI price=70: L bps=5.50×0.001/70×1e4=0.786  S bps=-51.55×0.001/70×1e4=-7.364
//
// BRENT: L=+12.20 S=-94.15 point=0.001 CS=1000
//
//	BRENT price=75: L bps=12.20×0.001/75×1e4=1.627  S bps=-94.15×0.001/75×1e4=-12.553
func TestMT5SwapMode9_InPointsNormalize(t *testing.T) {
	cases := []struct {
		name     string
		s, price float64
		wantBps  float64
	}{
		{"WTI long", 5.50, 70.0, 0.786},
		{"WTI short", -51.55, 70.0, -7.364},
		{"BRENT long", 12.20, 75.0, 1.627},
		{"BRENT short", -94.15, 75.0, -12.553},
	}
	for _, c := range cases {
		got, ok := normalizeSwapBpsDaily(SwapModeInPoints, c.s, c.price, 0.001, 1000)
		if !ok {
			t.Fatalf("%s: normalize should succeed", c.name)
		}
		if math.Abs(got-c.wantBps) > 0.001 {
			t.Errorf("%s: bps = %.4f, want ~%.3f", c.name, got, c.wantBps)
		}
	}
}

// TestSwapInPointsGuardRail D-092：护栏阈值边界——每手日 swap 美元额 |s|×point×CS
// 超 $500/手/日 → warn（返回 true）；未超 → false。WTI/BRENT 现值 $5~94 不触发。
func TestSwapInPointsGuardRail(t *testing.T) {
	cases := []struct {
		name         string
		s, point, cs float64
		want         bool
	}{
		{"WTI short 现值 $51.55 不触发", -51.55, 0.001, 1000, false},
		{"BRENT short 现值 $94.15 不触发", -94.15, 0.001, 1000, false},
		{"WTI long 现值 $5.50 不触发", 5.50, 0.001, 1000, false},
		{"边界恰好 500 不触发（>非>=）", 500.0, 1, 1, false},
		{"边界 500.01 触发", 500.01, 1, 1, true},
		{"大值 $1000 触发", 1000, 0.001, 1000, true},
		{"cs=0 安全返回 false", 999, 0.001, 0, false},
		{"point=0 安全返回 false", 999, 0, 1000, false},
	}
	for _, c := range cases {
		got := swapInPointsGuardRail("mt5", "fxpro", "WTI", c.s, c.point, c.cs, 0)
		if got != c.want {
			t.Errorf("%s: guard = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestPXBT_SwapTypeZero_AsInPoints D-159 W4a S2：venue=1314615(PXBT) SwapType=0 + 非零值 → InPoints override。
// 证据：EURUSD long=-3.6961 点（PXBT 实证），同量级同向于 Exness/IC/FxPro。
func TestPXBT_SwapTypeZero_AsInPoints(t *testing.T) {
	got := mt5SwapModeForVenue("1314615", mt5pb.SwapType_SwapType_SwapNone, -3.6961, 0)
	if got != SwapModeInPoints {
		t.Errorf("PXBT SwapType=0+非零值: mode=%v, want SwapModeInPoints", got)
	}
}

// TestPXBT_SwapTypeZero_ZeroValue_NotOverride D-159 W4a S2：venue=1314615 SwapType=0 + 零值 → 不 override（走 mt5SwapMode 原逻辑=SwapModeNone）。
func TestPXBT_SwapTypeZero_ZeroValue_NotOverride(t *testing.T) {
	got := mt5SwapModeForVenue("1314615", mt5pb.SwapType_SwapType_SwapNone, 0, 0)
	if got != SwapModeNone {
		t.Errorf("PXBT SwapType=0+零值: mode=%v, want SwapModeNone（不 override）", got)
	}
}

// TestNonPXBT_SwapTypeZero_NotOverride D-159 W4a S2：venue 非 PXBT SwapType=0 + 非零值 → 不 override（走 mt5SwapMode 原逻辑=Uncalibrated 矛盾数据）。
func TestNonPXBT_SwapTypeZero_NotOverride(t *testing.T) {
	got := mt5SwapModeForVenue("other", mt5pb.SwapType_SwapType_SwapNone, 1.0, 0)
	if got != SwapModeUncalibrated {
		t.Errorf("非 PXBT SwapType=0+非零值: mode=%v, want SwapModeUncalibrated（不 override）", got)
	}
}

// TestSwapFreeServerOverride D-300 S0 定案：Exness 白名单 venue 全品种 swap 置 0——
// 规格 XAUUSDm SwapLong=-534.9（名义标价账单不执行）不得污染 canonical（3001157 实证）。
func TestSwapFreeServerOverride(t *testing.T) {
	if !swapFreeServer("Exness-MT5Real") || !swapFreeServer("exness-mt5trial5") {
		t.Error("Exness 前缀 server 应命中白名单（大小写不敏感）")
	}
	if swapFreeServer("ICMarketsSC-Demo") || swapFreeServer("FxPro-MT5 Demo") {
		t.Error("非白名单 server 不应命中")
	}
	// 归一化端到端：白名单下 mode 覆盖为 None → 两方向恒 0
	l, okL := normalizeSwapBpsDaily(SwapModeNone, -534.9, 4287, 0.01, 100)
	s, okS := normalizeSwapBpsDaily(SwapModeNone, 0, 4287, 0.01, 100)
	if !okL || !okS || l != 0 || s != 0 {
		t.Errorf("SwapModeNone 归一应恒 0（got l=%v ok=%v, s=%v ok=%v）", l, okL, s, okS)
	}
}
