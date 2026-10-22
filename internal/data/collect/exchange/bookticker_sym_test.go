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

// 存在性闸锚：validPerp 未建立=fail-open；已建=目录不载形不产 desired
// （xpdusdusdt/clusdtusdt 幽灵流实证——tradifi 名 normalize 形对≠venue 存在）。
func TestPerpOKGate(t *testing.T) {
	b := &BinanceMarketWS{}
	if !b.perpOK("xagusdt") {
		t.Fatal("目录未建应 fail-open")
	}
	b.validPerp = map[string]struct{}{"BTCUSDT": {}, "XAGUSDT": {}}
	if !b.perpOK("xagusdt") {
		t.Fatal("目录在载 XAGUSDT 应放行")
	}
	if b.perpOK("xpdusdusdt") || b.perpOK("clusdtusdt") {
		t.Fatal("目录不载幽灵形应隔离")
	}
}
