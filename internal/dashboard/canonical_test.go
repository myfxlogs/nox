package dashboard

import "testing"

// TestCanonicalize 验证 D-056 统一映射：BTC/ETH 的 USD 报价 → USDT 后缀；其余原样。
func TestCanonicalize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"BTCUSD", "BTCUSDT"},
		{"ETHUSD", "ETHUSDT"},
		{"EURUSD", "EURUSD"}, // 外汇原样
		{"GBPUSD", "GBPUSD"},
		{"XAUUSD", "XAUUSD"}, // 金属原样
		{"GOLD", "GOLD"},     // 别名原样（非 curated map 命中）
		{"WTI", "WTI"},       // 能源原样
		{"TRXUSDT", "TRXUSDT"}, // 已 USDT 后缀原样
		{"", ""},             // 空串原样
		{"btcusd", "btcusd"}, // 大小写敏感：curated map 仅命中大写 BTCUSD
	}
	for _, c := range cases {
		if got := Canonicalize(c.in); got != c.want {
			t.Fatalf("Canonicalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
