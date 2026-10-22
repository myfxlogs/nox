package exchange

import "testing"

// indiv normalize 锚（XAGUSDT 死流实证）：计价尾透传不再拼/裸基补 usdt/
// 非法形 quarantine 语义由 spotOK/instruments 目录承担（normalize 不吞）。
func TestBookTickerStreamSym(t *testing.T) {
	for in, want := range map[string]string{
		"XAGUSDT":  "xagusdt",
		"BTC":      "btcusdt",
		"UB":       "ubusdt",
		"ETHUSDC":  "ethusdc",
		"SOLFDUSD": "solfdusd",
		"trxusdt":  "trxusdt",
	} {
		if got := bookTickerStreamSym(in); got != want {
			t.Fatalf("bookTickerStreamSym(%q)=%q want %q", in, got, want)
		}
	}
}
