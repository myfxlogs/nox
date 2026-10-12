// d420_symbols_test.go —— D-420 S1/S4：采集基集扩容回归（SPX 家族三所对称的最小符号面）。
package main

import "testing"

func TestD420_DefaultCexSymbols_Expanded(t *testing.T) {
	have := map[string]bool{}
	for _, s := range defaultCexSymbols {
		have[s] = true
	}
	// D-420 S1：+SPX（binance SPXUSDT/okx SPX-USDT-SWAP/gate SPX_USDT 的 funding 三所对称补盲）
	if !have["SPX"] {
		t.Errorf("cexSymbols 缺 SPX（SPX 家族 funding 三所补盲）: %v", defaultCexSymbols)
	}
	// 既有成员回归（D-300 S2/D-303/D-308 扩容面不回退）
	for _, want := range []string{"BTC", "ETH", "TRX", "PAXG", "XAUT", "XAU", "COPPER", "XAG", "CL", "BZ", "XPT", "XPD", "NAS100", "US30", "SPX500", "JP225"} {
		if !have[want] {
			t.Errorf("既有符号 %s 丢失", want)
		}
	}
}
