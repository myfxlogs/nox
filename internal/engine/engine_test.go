package engine

import "testing"

// TestCommissionFor 按 venueType 分档佣金（D-035：禁硬编码，且修正旧实现 gate 漏配 bug）。
func TestCommissionFor(t *testing.T) {
	e := &Engine{comm: Commission{CFDBps: 0.3, CEXTakerBps: 10.0}}
	cases := map[string]float64{
		"277842155:mt5": 0.3, // CFD
		"REDACTED_MT_ACCT:mt4": 0.3, // CFD
		"binance:spot":  10.0,
		"binance:perp":  10.0,
		"okx:spot":      10.0,
		"okx:perp":      10.0,
		"gate:spot":     10.0, // 旧实现只匹配 binance/okx → gate 落到 0.3，须为 10
		"gate:perp":     10.0,
	}
	for k, want := range cases {
		if got := e.commissionFor(k); got != want {
			t.Errorf("commissionFor(%q) = %v, want %v", k, got, want)
		}
	}
}
