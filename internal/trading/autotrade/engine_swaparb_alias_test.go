package autotrade

// 单测：alias 全面化——aliasForVenueKey 纯函数 + aliasOrKey（store nil 回退）。

import "testing"

// 用例①：aliasForVenueKey 纯函数——有别名返回别名，无别名返回空（前端回退 venueKey）。
func TestAliasForVenueKey(t *testing.T) {
	aliasMap := map[string]string{"529": "ICMarkets", "277": "Pepperstone"}
	tests := []struct {
		name     string
		venueKey string
		want     string
	}{
		{"有别名 mt5", "529:mt5", "ICMarkets"},
		{"有别名 mt4", "277:mt4", "Pepperstone"},
		{"无别名", "999:mt5", ""}, // 前端回退 venueKey
		{"无冒号整串查", "529", "ICMarkets"},
		{"无冒号无别名", "999", ""},
		{"空 venueKey", "", ""},
		{"空 aliasMap", "529:mt5", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m map[string]string
			if tt.name != "空 aliasMap" {
				m = aliasMap
			}
			got := aliasForVenueKey(m, tt.venueKey)
			if got != tt.want {
				t.Errorf("aliasForVenueKey(%q) = %q, want %q", tt.venueKey, got, tt.want)
			}
		})
	}
}

// 用例②：aliasOrKey store nil → 回退 venueKey（不 panic）。
func TestAliasOrKey_NilStore(t *testing.T) {
	e := &Engine{} // store nil
	got := e.aliasOrKey("529:mt5")
	if got != "529:mt5" {
		t.Errorf("store nil 应回退 venueKey，got %q, want %q", got, "529:mt5")
	}
}
