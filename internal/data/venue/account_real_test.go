// account_real_test.go —— 账户行口径纯函数锚（自 cexscan 随函数下沉，D-360/D-361/D-363）。
package venue

import "testing"

// TestIsRealAccount D-360 S5 判定锚：五态全覆盖。
func TestIsRealAccount(t *testing.T) {
	realKeys := map[string]bool{"3001157": true, "1279967": true}
	cases := []struct {
		name      string
		assetType string
		simulated bool
		venue     string
		want      bool
	}{
		{"cex+sim → 丢（v2 原生键，gate/binance-perp sim 实证）", "cex", true, "gate", false},
		{"cex+real → 留", "cex", false, "okx", true},
		{"cfd+is_real → 留", "cfd", false, "3001157", true},
		{"cfd+demo → 丢", "cfd", false, "52993526", false},
		{"未知 type → 丢", "paper", false, "x", false},
		{"cfd 键集 nil → fail-closed 丢", "cfd", false, "3001157", false},
	}
	for _, c := range cases {
		keys := realKeys
		if c.name == "cfd 键集 nil → fail-closed 丢" {
			keys = nil
		}
		if got := IsRealAccount(c.assetType, c.simulated, c.venue, keys); got != c.want {
			t.Errorf("%s: IsRealAccount(%q,%v,%q) = %v, want %v", c.name, c.assetType, c.simulated, c.venue, got, c.want)
		}
	}
}

// TestNormalizeVenueType 归一锚：okx cex → unified；binance/gate 现货合约分钱包不归一；
// 非 cex 不归一。
func TestNormalizeVenueType(t *testing.T) {
	cases := []struct {
		name                    string
		assetType, venue, vType string
		want                    string
	}{
		{"okx 统一账户归一", "cex", "okx", "spot", "unified"},
		{"okx perp 同归 unified", "cex", "okx", "perp", "unified"},
		{"binance 分钱包不归一", "cex", "binance", "spot", "spot"},
		{"gate 分钱包不归一", "cex", "gate", "perp", "perp"},
		{"非 cex 不归一", "cfd", "okx", "mt5", "mt5"},
	}
	for _, c := range cases {
		if got := NormalizeVenueType(c.assetType, c.venue, c.vType); got != c.want {
			t.Errorf("%s: NormalizeVenueType(%q,%q,%q) = %q, want %q", c.name, c.assetType, c.venue, c.vType, got, c.want)
		}
	}
}
