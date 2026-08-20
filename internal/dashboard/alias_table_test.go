package dashboard

import "testing"

func TestMatchCanonical(t *testing.T) {
	tests := []struct {
		symbol string
		canon  string
		hits   int
	}{
		// 精确匹配
		{"EURUSD", "EURUSD", 1},
		{"GOLD", "XAUUSD", 1},
		{"TRON", "TRXUSDT", 1},
		{"UKOIL", "BRENT", 1},
		{"USOIL", "WTI", 1},
		// Cent 后缀变体
		{"EURUSDm", "EURUSD", 1},
		{"BTCUSDm", "BTCUSDT", 1},
		{"XAUUSDm", "XAUUSD", 1},
		// 大小写不敏感
		{"eurusd", "EURUSD", 1},
		{"gold", "XAUUSD", 1},
		// 无匹配
		{"JPMm", "", 0},
		{"AAPL", "", 0},
		{"STOCK123", "", 0},
	}
	for _, tt := range tests {
		canon, hits := matchCanonicalDistinct(tt.symbol)
		if hits != tt.hits {
			t.Errorf("matchCanonicalDistinct(%q) hits = %d, want %d", tt.symbol, hits, tt.hits)
		}
		if hits > 0 && canon != tt.canon {
			t.Errorf("matchCanonicalDistinct(%q) canon = %q, want %q", tt.symbol, canon, tt.canon)
		}
	}
}

func TestCanonicalAliasesCoverage(t *testing.T) {
	// 验证 alias 表覆盖全部已映射 canonical（4 broker 实证）
	required := []string{
		"EURUSD", "GBPUSD", "AUDUSD", "USDJPY", "USDCHF", "USDCAD",
		"NZDUSD", "EURJPY", "GBPJPY", "EURGBP",
		"BTCUSDT", "ETHUSDT", "TRXUSDT",
		"XAUUSD", "SILVER",
		"BRENT", "WTI",
		"US30", "NAS100", "SPX500", "GER40",
	}
	for _, c := range required {
		if _, ok := canonicalAliases[c]; !ok {
			t.Errorf("canonicalAliases 缺少 %s", c)
		}
	}
}

func TestAllCanonicalsSorted(t *testing.T) {
	cs := allCanonicals()
	if len(cs) != len(canonicalAliases) {
		t.Errorf("allCanonicals len = %d, want %d", len(cs), len(canonicalAliases))
	}
	for i := 1; i < len(cs); i++ {
		if cs[i-1] > cs[i] {
			t.Errorf("allCanonicals 未排序: %s > %s", cs[i-1], cs[i])
		}
	}
}
