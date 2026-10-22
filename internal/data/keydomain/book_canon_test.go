package keydomain

import "testing"

// TestBookCanonical 方言归一锚（S2 #26-31 病灶族）：下划线/连字符/-SWAP 尾/
// 裸名→归一形；归一形幂等；USDT/USD 尾透传。
func TestBookCanonical(t *testing.T) {
	for in, want := range map[string]string{
		"XAG_USDT":      "XAGUSDT",
		"XAG-USDT-SWAP": "XAGUSDT",
		"NAS100_USDT":   "NAS100USDT",
		"US30_USDT":     "US30USDT",
		"SPX500_USDT":   "SPX500USDT",
		"ETH":           "ETHUSDT",
		"XAUUSDT":       "XAUUSDT",
		"TRXUSDT":       "TRXUSDT",
	} {
		if got := BookCanonical(in); got != want {
			t.Fatalf("BookCanonical(%q)=%q want %q", in, got, want)
		}
	}
}
