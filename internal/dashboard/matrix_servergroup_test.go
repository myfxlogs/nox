package dashboard

import "testing"

// TestServerGroup D-150 ② 平台分组键=server 首段（首个 '-' 前）。
// D-154 S4：实例快照改 DB 真实 10 唯一 server（2026-08-30 实证），修正三处失真：
// FxPro MT5 Demo→FxPro-MT5 Demo（带连字符归 FxPro 组）、补 Exness-MT5Real5+ICMarketsSC-MT5-6 两实盘、删幻影 PXBTTrading-2。
func TestServerGroup(t *testing.T) {
	cases := []struct {
		server string
		want   string
	}{
		{"Exness-MT5Real5", "Exness"},
		{"Exness-MT5Trial5", "Exness"},
		{"ICMarketsSC-Demo", "ICMarketsSC"},
		{"ICMarketsSC-MT5-6", "ICMarketsSC"},
		{"PXBTTrading-1", "PXBTTrading"},
		{"CMCMarkets-MT5-DEMO", "CMCMarkets"},
		{"HantecMarketsMU-MT5", "HantecMarketsMU"},
		{"CPTMarkets-Live04", "CPTMarkets"},
		{"Pepperstone-Demo", "Pepperstone"},
		{"FxPro-MT5 Demo", "FxPro"}, // 带连字符 → 归 FxPro 组（原 FxPro MT5 Demo 无连字符=独立组，失真已修）
		{"", ""},                    // 空 server → 空组键
		{"NoDash", "NoDash"},        // 无 '-' → 整段
	}
	for _, c := range cases {
		got := serverGroup(c.server)
		if got != c.want {
			t.Errorf("serverGroup(%q) = %q, want %q", c.server, got, c.want)
		}
	}
}

// TestServerGroup_RealNetwork10UniqueServers 现网 10 唯一 server（11 账号，PXBTTrading-1 ×2 账号）→ 8 平台组。
// 8 组：Exness(2 server)/ICMarketsSC(2 server)/PXBTTrading/CMCMarkets/HantecMarketsMU/CPTMarkets/Pepperstone/FxPro。
func TestServerGroup_RealNetwork10UniqueServers(t *testing.T) {
	servers := []string{
		"Exness-MT5Real5",
		"Exness-MT5Trial5",
		"ICMarketsSC-Demo",
		"ICMarketsSC-MT5-6",
		"PXBTTrading-1", // ×2 账号同 server，唯一 server 列一次
		"CMCMarkets-MT5-DEMO",
		"HantecMarketsMU-MT5",
		"CPTMarkets-Live04",
		"Pepperstone-Demo",
		"FxPro-MT5 Demo",
	}
	groups := make(map[string]bool)
	for _, s := range servers {
		groups[serverGroup(s)] = true
	}
	want := 8
	if len(groups) != want {
		t.Errorf("现网 10 唯一 server 分组 want %d 组, got %d (%v)", want, len(groups), groups)
	}
}
