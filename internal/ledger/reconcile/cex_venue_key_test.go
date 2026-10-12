// cex_venue_key_test.go —— D-362 S6：撞键归一纯函数锚。
package reconcile

import "testing"

// TestCexVenueKey D-362 S6 锚：双 config 组赢家 canonical+输家 #id；单 config 原名；
// 同 type 不同 venue 互不影响。
func TestCexVenueKey(t *testing.T) {
	// binance:perp 双 config：赢家 id25（sim 后建恒赢=今日事实显式化）
	winners := map[string]int64{"binance:perp": 25}
	cases := []struct {
		name             string
		venue, venueType string
		cfgID            int64
		want             string
	}{
		{"赢家保 canonical", "binance", "perp", 25, "binance:perp"},
		{"输家加 #id 后缀", "binance", "perp", 20, "binance:perp#20"},
		{"无撞键单 config 原名", "gate", "perp", 24, "gate:perp"},
		{"同 type 不同 venue 互不影响", "okx", "perp", 1, "okx:perp"},
		{"winners nil（未启用归一）原名", "binance", "perp", 20, "binance:perp"},
	}
	for _, c := range cases {
		w := winners
		if c.name == "winners nil（未启用归一）原名" {
			w = nil
		}
		if got := CexVenueKey(c.venue, c.venueType, c.cfgID, w); got != c.want {
			t.Errorf("%s: CexVenueKey(%q,%q,%d) = %q, want %q", c.name, c.venue, c.venueType, c.cfgID, got, c.want)
		}
	}
}
