package dashboard

import (
	"testing"
)

// D-127 P2：cexStepMeta + fallback + fail-closed 单测。

func TestCexStepSizeFallback_KnownSymbols(t *testing.T) {
	cases := []struct {
		symbol string
		want   float64
	}{
		{"BTCUSDT", 0.001},
		{"ETHUSDT", 0.001},
		{"DOTUSDT", 0.1},  // D-127 修正：旧表 0.01 → 实测 0.1
		{"XRPUSDT", 0.1},  // D-127 修正：旧表 1 → 实测 0.1
		{"TRXUSDT", 1},    // D-125 实证
		{"BNBUSDT", 0.01}, // 探测实证
		{"DOGEUSDT", 1},
		{"SOLUSDT", 0.01},
	}
	for _, c := range cases {
		if got := cexStepSizeFallback(c.symbol); got != c.want {
			t.Errorf("cexStepSizeFallback(%s)=%v，期望 %v", c.symbol, got, c.want)
		}
	}
}

func TestCexStepSizeFallback_OKXFormat(t *testing.T) {
	// OKX perp 格式 BTC-USDT-SWAP → base=BTC → 0.001
	if got := cexStepSizeFallback("BTC-USDT-SWAP"); got != 0.001 {
		t.Errorf("cexStepSizeFallback(BTC-USDT-SWAP)=%v，期望 0.001", got)
	}
	if got := cexStepSizeFallback("DOT-USDT-SWAP"); got != 0.1 {
		t.Errorf("cexStepSizeFallback(DOT-USDT-SWAP)=%v，期望 0.1", got)
	}
}

func TestCexStepSizeFallback_UnknownFailClosed(t *testing.T) {
	// 未知品种 → 0（fail-closed，D-127 P2 删除「未知默认 0.01」猜测）
	if got := cexStepSizeFallback("FOOBARUSDT"); got != 0 {
		t.Errorf("cexStepSizeFallback(FOOBARUSDT)=%v，期望 0（fail-closed）", got)
	}
}

func TestCexStepMeta_RuntimeOverridesFallback(t *testing.T) {
	// 运行时表有值 → 优先于 fallback
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

func TestCexStepMeta_FallbackWhenRuntimeEmpty(t *testing.T) {
	// 运行时表空 → 回退到硬编码
	m := NewCexStepMeta("http://unused", 0)
	if got := m.step("DOTUSDT"); got != 0.1 {
		t.Errorf("step(DOTUSDT) fallback=%v，期望 0.1", got)
	}
}

func TestCexStepMeta_UnknownReturnsZero(t *testing.T) {
	// 运行时表 + fallback 均无 → 0（fail-closed）
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
