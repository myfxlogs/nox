package dashboard

import (
	"testing"

	"arb/internal/data/bus"
)

// TestCexCanonical D-311 R6④：CEX 符号→canonical 派生镜像采集器归一规则。
// gate=ReplaceAll("_","")（CL_USDT→CLUSDT，修旧码漏剥 _ 致现价「—」）；
// okx=TrimSuffix("-SWAP")+ReplaceAll("-","")；binance=直通。
func TestCexCanonical(t *testing.T) {
	cases := []struct{ symbol, want string }{
		{"CL_USDT", "CLUSDT"},        // gate perp（旧码得 CL_USDT → qbus miss）
		{"BTC_USDT", "BTCUSDT"},      // gate spot
		{"TRX-USDT-SWAP", "TRXUSDT"}, // okx perp
		{"BTC-USDT", "BTCUSDT"},      // okx spot
		{"BTCUSDT", "BTCUSDT"},       // binance 直通不变
		{"TRXUSDT", "TRXUSDT"},
	}
	for _, c := range cases {
		if got := bus.CexSymbolKey(c.symbol); got != c.want {
			t.Errorf("bus.CexSymbolKey(%q)=%q, want %q", c.symbol, got, c.want)
		}
	}
}
