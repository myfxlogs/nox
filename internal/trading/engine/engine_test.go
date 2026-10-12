package engine

import (
	"strings"
	"testing"

	"arb/internal/research/evaluator"
)

// testCommissionResolver 复刻 main 装配的分档逻辑（D-036 per-venue：CFD 账号值/CEX 逐所 taker，回退默认）。
func testCommissionResolver(t *testing.T, cfd, cex map[string]float64) func(string) float64 {
	return func(venueKey string) float64 {
		venue := venueKey[:strings.IndexByte(venueKey, ':')]
		if bps, ok := cfd[venue]; ok {
			return bps
		}
		if strings.HasSuffix(venueKey, ":mt4") || strings.HasSuffix(venueKey, ":mt5") {
			return 0.3
		}
		if bps, ok := cex[venue]; ok {
			return bps
		}
		return 10.0
	}
}

// TestCommissionResolver 按 venue 解析佣金（D-036：CFD 账号真实值优先，CEX 逐所 taker）。
func TestCommissionResolver(t *testing.T) {
	cfd := map[string]float64{"277842155": 0.7}
	cex := map[string]float64{"gate": 5.0}
	commFor := testCommissionResolver(t, cfd, cex)
	cases := map[string]float64{
		"277842155:mt5":        0.7, // 账号真实佣金
		"REDACTED_MT_ACCT:mt4": 0.3, // 无账号值 → CFD 默认
		"binance:spot":         10.0,
		"okx:perp":             10.0,
		"gate:spot":            5.0, // 逐所 taker
		"gate:perp":            5.0,
	}
	for k, want := range cases {
		if got := commFor(k); got != want {
			t.Errorf("commission(%q) = %v, want %v", k, got, want)
		}
	}
}

// TestNewRequiresResolver 佣金解析器必填（成本正确性依赖真实佣金，D-036）。
func TestNewRequiresResolver(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil commission resolver")
		}
	}()
	New(nil, nil, evaluator.Config{}, nil, nil, nil, 0.5, nil, 0, 0, 0, 0, 0)
}
