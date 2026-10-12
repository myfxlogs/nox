package venue

import "testing"

func TestHasPrivateStream(t *testing.T) {
	cases := []struct {
		venue, vt string
		sim       bool
		want      bool
	}{
		{"gate", "perp", false, true},
		{"gate", "spot", false, false},
		{"binance", "perp", false, true},
		{"binance", "perp", true, true},
		{"binance", "spot", false, true},
		{"binance", "spot", true, false}, // demo spot ws-api 不在代理路由内
		{"binance", "funding", false, false},
		{"okx", "perp", false, true},
		{"okx", "spot", false, true}, // 统一账户流覆盖
		{"okx", "perp", true, true},
		{"okx", "funding", false, false},
		{"okx", "earn", false, false},
		{"3001157", "mt5", false, false}, // cfd/MT 无私有流写口
		{"cmc", "cfd", false, false},
	}
	for _, c := range cases {
		if got := HasPrivateStream(c.venue, c.vt, c.sim); got != c.want {
			t.Errorf("HasPrivateStream(%q,%q,%v) = %v, want %v", c.venue, c.vt, c.sim, got, c.want)
		}
	}
}
