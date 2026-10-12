package verification

import (
	"math"
	"testing"
)

func TestExpectedUsd(t *testing.T) {
	cases := []struct {
		name         string
		typ          string
		netBps       float64
		swapBpsDaily float64
		notionalUSD  float64
		holdDays     float64
		want         float64
	}{
		// CROSS_VENUE 一次性 bps：netBps/10000 × notional
		{"CROSS_VENUE 正期望", "CROSS_VENUE", 10.0, 0, 1000, 0, 10.0 / 10000 * 1000},
		{"MEANREVERT 正期望", "MEANREVERT", 20.0, 0, 500, 0, 20.0 / 10000 * 500},
		{"CROSS_VENUE 负期望（事故）", "CROSS_VENUE", -5.0, 0, 1000, 0, -5.0 / 10000 * 1000},

		// FUNDING 族年化%×天数/365
		{"FUNDING_HEDGE 正期望", "FUNDING_HEDGE", 15.0, 0, 1000, 30, 15.0 / 100 * 1000 * 30 / 365},
		{"PERP_FUNDING 正期望", "PERP_FUNDING", 5.0, 0, 2000, 10, 5.0 / 100 * 2000 * 10 / 365},
		{"CARRY 正期望", "CARRY", 8.0, 0, 1000, 365, 8.0 / 100 * 1000 * 365 / 365},
		{"FUNDING_HEDGE 负期望", "FUNDING_HEDGE", -2.0, 0, 1000, 30, 0}, // D-087：netBps≤0 → 0（防负期望入统计）
		{"FUNDING_HEDGE holdDays=0", "FUNDING_HEDGE", 15.0, 0, 1000, 0, 0},

		// XASSET_CARRY 归 FUNDING 族分支（net_bps=年化百分点语义，pos 488 expected_net_bps=8.10 同口径）
		{"XASSET_CARRY 正期望", "XASSET_CARRY", 8.0, 0, 4364, 1, 8.0 / 100 * 4364 * 1 / 365},
		{"XASSET_CARRY 负期望", "XASSET_CARRY", -2.0, 0, 1000, 30, 0},

		// SWAPARB 日 bps×天数（D-087：用 swapBpsDaily，netBps 不参与）
		{"SWAPARB 正期望", "SWAPARB", 0, 0.5, 1000, 10, 0.5 / 10000 * 1000 * 10},
		{"SWAPARB swapBpsDaily=0", "SWAPARB", 0, 0, 1000, 5, 0}, // D-087：swapBpsDaily≤0 → 0
		{"SWAPARB holdDays=0", "SWAPARB", 0, 0.5, 1000, 0, 0},

		// 边界
		{"notional=0", "CROSS_VENUE", 10.0, 0, 0, 0, 0},
		{"notional<0", "CROSS_VENUE", 10.0, 0, -100, 0, 0},
		{"未知类型", "UNKNOWN", 10.0, 0, 1000, 10, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpectedUsd(tc.typ, tc.netBps, tc.swapBpsDaily, tc.notionalUSD, tc.holdDays)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("ExpectedUsd(%s, %v, %v, %v, %v) = %v, want %v", tc.typ, tc.netBps, tc.swapBpsDaily, tc.notionalUSD, tc.holdDays, got, tc.want)
			}
		})
	}
}

func TestIsCarryType(t *testing.T) {
	cases := []struct {
		typ  string
		want bool
	}{
		{"SWAPARB", true},
		{"CARRY", true},
		{"FUNDING_HEDGE", true},
		{"PERP_FUNDING", true},
		{"XASSET_CARRY", true}, // D-300：息差家族（net=年化百分点）
		{"CROSS_VENUE", false},
		{"MEANREVERT", false},
		{"UNKNOWN", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsCarryType(tc.typ); got != tc.want {
			t.Errorf("IsCarryType(%q) = %v, want %v", tc.typ, got, tc.want)
		}
	}
}

func TestVerifyLevel(t *testing.T) {
	cases := []struct {
		name       string
		samples    int
		minSamples int
		ratio      float64
		minRatio   float64
		fresh      bool
		want       int
	}{
		{"V1 正常通过", 3, 2, 0.85, 0.7, true, LevelV1},
		{"V1 恰好门槛", 2, 2, 0.7, 0.7, true, LevelV1},
		{"V0 样本不足", 1, 2, 0.9, 0.7, true, LevelV0},
		{"V0 R 低于门槛", 3, 2, 0.5, 0.7, true, LevelV0},
		{"V0 不新鲜", 3, 2, 0.9, 0.7, false, LevelV0},
		{"V0 ratio=NaN", 3, 2, math.NaN(), 0.7, true, LevelV0},
		{"V0 minSamples=0 但 R<门槛", 0, 0, 0.5, 0.7, true, LevelV0},
		{"V1 minSamples=0 且 R≥门槛", 1, 0, 0.8, 0.7, true, LevelV1},
		{"V1 门槛关闭（minRatio=0）", 1, 0, 0.1, 0, true, LevelV1},
		{"V1 全关闭仅凭 fresh", 0, 0, 0.01, 0, true, LevelV1},
		{"V0 不新鲜+门槛关闭", 0, 0, 0.01, 0, false, LevelV0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := VerifyLevel(tc.samples, tc.minSamples, tc.ratio, tc.minRatio, tc.fresh)
			if got != tc.want {
				t.Errorf("VerifyLevel(%d, %d, %v, %v, %v) = %d, want %d", tc.samples, tc.minSamples, tc.ratio, tc.minRatio, tc.fresh, got, tc.want)
			}
		})
	}
}

func TestVkey(t *testing.T) {
	got := Vkey("CROSS_VENUE", "binance:spot", "okx:perp")
	want := "CROSS_VENUE:binance:spot:okx:perp"
	if got != want {
		t.Errorf("Vkey() = %q, want %q", got, want)
	}
}

func TestVkeyFromOppID(t *testing.T) {
	cases := []struct {
		oppID string
		want  string
	}{
		// 新格式（6 段组合键）
		{"CROSS_VENUE:BTCUSDT:binance:spot:okx:perp", "CROSS_VENUE:binance:spot:okx:perp"},
		{"FUNDING_HEDGE:ETHUSDT:binance:spot:okx:perp", "FUNDING_HEDGE:binance:spot:okx:perp"},
		{"SWAPARB:AUDJPY:icmarkets:mt5:pepperstone:mt5", "SWAPARB:icmarkets:mt5:pepperstone:mt5"},
		// 旧格式（7 段尾时间戳）
		{"CROSS_VENUE:BTCUSDT:binance:spot:okx:perp:1692904800000", "CROSS_VENUE:binance:spot:okx:perp"},
		// 异常
		{"", ""},
		{"abc", ""},
		{"a:b:c", ""},
	}
	for _, tc := range cases {
		got := VkeyFromOppID(tc.oppID)
		if got != tc.want {
			t.Errorf("VkeyFromOppID(%q) = %q, want %q", tc.oppID, got, tc.want)
		}
	}
}
