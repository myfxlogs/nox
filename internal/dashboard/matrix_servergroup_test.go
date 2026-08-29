package dashboard

import "testing"

// TestServerGroup D-150 ② 平台分组键=server 首段（首个 '-' 前）。
// 用现网 11 账号 server 实例断言 8 组（自审记录验证成立）。
func TestServerGroup(t *testing.T) {
	cases := []struct {
		server string
		want   string
	}{
		{"Exness-MT5Trial5", "Exness"},
		{"ICMarketsSC-Demo", "ICMarketsSC"},
		{"PXBTTrading-1", "PXBTTrading"},
		{"PXBTTrading-2", "PXBTTrading"}, // 同组（两 PXBT 账号）
		{"CMCMarkets-MT5-DEMO", "CMCMarkets"},
		{"HantecMarketsMU-MT5", "HantecMarketsMU"},
		{"CPTMarkets-Live04", "CPTMarkets"},
		{"Pepperstone-Demo", "Pepperstone"},
		{"FxPro MT5 Demo", "FxPro MT5 Demo"}, // 无 '-' → 整段
		{"", ""},                             // 空 server → 空组键
		{"NoDash", "NoDash"},                 // 无 '-' → 整段
	}
	for _, c := range cases {
		got := serverGroup(c.server)
		if got != c.want {
			t.Errorf("serverGroup(%q) = %q, want %q", c.server, got, c.want)
		}
	}
}

// TestServerGroup_RealNetwork11Accounts 现网 11 账号实例 → 8 平台组（自审记录）。
func TestServerGroup_RealNetwork11Accounts(t *testing.T) {
	servers := []string{
		"Exness-MT5Trial5",
		"ICMarketsSC-Demo",
		"PXBTTrading-1",
		"PXBTTrading-2",
		"CMCMarkets-MT5-DEMO",
		"HantecMarketsMU-MT5",
		"CPTMarkets-Live04",
		"Pepperstone-Demo",
		"FxPro MT5 Demo",
		// 11 账号含重复 server（同 broker 多账号），此处列 9 个唯一 server 代表 11 账号
	}
	groups := make(map[string]bool)
	for _, s := range servers {
		groups[serverGroup(s)] = true
	}
	// 8 平台组：Exness / ICMarketsSC / PXBTTrading / CMCMarkets / HantecMarketsMU / CPTMarkets / Pepperstone / FxPro MT5 Demo
	want := 8
	if len(groups) != want {
		t.Errorf("现网 server 分组 want %d 组, got %d (%v)", want, len(groups), groups)
	}
}
