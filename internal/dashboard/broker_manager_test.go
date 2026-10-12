package dashboard

import "testing"

// TestTopGroup 分组路径取"市场分组"段：跳过账号类型前缀（Exness Standard\Forex\EURUSDm→Forex），
// 无前缀取第一段（FxPro Forex\Major\EURUSD→Forex）。回归锁：账号类型前缀拍平 bug（D-042 后续修正）。
func TestTopGroup(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Exness 等：第一段 = 账号类型，市场分组在第二段
		{`Standard\Forex\EURUSDm`, "Forex"},
		{`Standard\Crypto\BTCUSDm`, "Crypto"},
		{`Standard\Stocks\AAPLm`, "Stocks"},
		{`Standard\Forex_Indicator\USDRUBm`, "Forex_Indicator"},
		{`Cent\Crypto\ETHUSDm`, "Crypto"}, // cent 账号同构
		{`Micro\Metals\XAUUSD`, "Metals"}, // micro 账号同构
		// FxPro 等：第一段即市场分组
		{`Forex\Major\EURUSD`, "Forex"},
		{`Metals\Gold\GOLD`, "Metals"},
		{`Cryptos\BITCOIN`, "Cryptos"},
		{`Stocks\United States\AAPL.O`, "Stocks"},
		{`Spot\Indices\Major\#US30`, "Spot"},
		// 边界
		{"", ""},
		{"FLAT", "FLAT"},         // 无分隔符
		{`Standard`, "Standard"}, // 全为账号类型 → 原样（不产生空组）
	}
	for _, c := range cases {
		if got := topGroup(c.in); got != c.want {
			t.Errorf("topGroup(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
