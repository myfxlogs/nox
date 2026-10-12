package dashboard

import (
	"testing"
)

// D-127 P2：cexStepMeta + fallback + fail-closed 单测。

func TestCexStepMeta_RuntimeTable(t *testing.T) {
	// D-466：运行时表是唯一源（硬编码 fallback 已撤）
	m := NewCexStepMeta("http://unused", 0)
	m.mu.Lock()
	m.rt = map[string]float64{"BTCUSDT": 0.002}
	m.mu.Unlock()
	if got := m.step("BTCUSDT"); got != 0.002 {
		t.Errorf("step(BTCUSDT) runtime=%v，期望 0.002", got)
	}
	// OKX 格式经 normalize 也能命中运行时表
	if got := m.step("BTC-USDT-SWAP"); got != 0.002 {
		t.Errorf("step(BTC-USDT-SWAP) runtime=%v，期望 0.002（normalize 命中）", got)
	}
}

func TestCexStepMeta_RuntimeEmptyFailClosed(t *testing.T) {
	// D-466：运行时表空 → 0（硬编码 fallback 已撤，fail-closed 由预检拒单）
	m := NewCexStepMeta("http://unused", 0)
	if got := m.step("DOTUSDT"); got != 0 {
		t.Errorf("step(DOTUSDT)=%v，期望 0（无 fallback）", got)
	}
}

func TestCexStepMeta_UnknownReturnsZero(t *testing.T) {
	// 运行时表无 → 0（fail-closed）
	m := NewCexStepMeta("http://unused", 0)
	if got := m.step("ZZZUSDT"); got != 0 {
		t.Errorf("step(ZZZUSDT)=%v，期望 0（fail-closed）", got)
	}
}

func TestParseBinanceExchangeInfo(t *testing.T) {
	// fixture JSON：2 个 symbol，各 1 个 LOT_SIZE filter + 1 个 PRICE_FILTER（应跳过）
	body := []byte(`{
		"symbols": [
			{
				"symbol": "BTCUSDT",
				"filters": [
					{"filterType": "PRICE_FILTER", "stepSize": "0.01"},
					{"filterType": "LOT_SIZE", "stepSize": "0.001"}
				]
			},
			{
				"symbol": "DOTUSDT",
				"filters": [
					{"filterType": "LOT_SIZE", "stepSize": "0.1"}
				]
			}
		]
	}`)
	parsed, err := parseBinanceExchangeInfo(body)
	if err != nil {
		t.Fatalf("parseBinanceExchangeInfo: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("期望 2 symbol，实际 %d", len(parsed))
	}
	if parsed["BTCUSDT"] != 0.001 {
		t.Errorf("BTCUSDT step=%v，期望 0.001", parsed["BTCUSDT"])
	}
	if parsed["DOTUSDT"] != 0.1 {
		t.Errorf("DOTUSDT step=%v，期望 0.1", parsed["DOTUSDT"])
	}
}

func TestCexNormalizeToBinance(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"BTC-USDT-SWAP", "BTCUSDT"},
		{"BTCUSDT", "BTCUSDT"},
		{"TRX-USDT-SWAP", "TRXUSDT"},
	}
	for _, c := range cases {
		if got := cexNormalizeToBinance(c.in); got != c.want {
			t.Errorf("cexNormalizeToBinance(%s)=%q，期望 %q", c.in, got, c.want)
		}
	}
}
