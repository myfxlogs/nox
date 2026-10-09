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
		// D-067 外汇交叉盘
		{"AUDCAD", "AUDCAD", 1},
		{"EURCHF", "EURCHF", 1},
		{"GBPCHF", "GBPCHF", 1},
		{"NZDJPY", "NZDJPY", 1},
		// D-067 加密全名变体（Pepperstone）
		{"SOLANA", "SOLUSDT", 1},
		{"XRP", "XRPUSDT", 1},
		{"DOGECOIN", "DOGEUSDT", 1},
		{"LITECOIN", "LTCUSDT", 1},
		{"CARDANO", "ADAUSDT", 1},
		{"BITCOIN", "BTCUSDT", 1},
		{"ETHEREUM", "ETHUSDT", 1},
		{"BINANCECOIN", "BNBUSDT", 1},
		// D-067 加密 USD 后缀经 Canonicalize 归一
		{"BNBUSD", "BNBUSDT", 1},
		{"SOLUSD", "SOLUSDT", 1},
		{"XRPUSD", "XRPUSDT", 1},
		{"AVXUSD", "AVAXUSDT", 1}, // FxPro 缩写 AVXUSD → AVAXUSDT
		{"LNKUSD", "LINKUSDT", 1}, // FxPro 缩写 LNKUSD → LINKUSDT
		// D-067 金属全名变体
		{"PALLADIUM", "XPDUSD", 1},
		{"PLATINUM", "XPTUSD", 1},
		{"COPPER", "XCUUSD", 1},
		{"ZINC", "XZNUSD", 1},
		// D-067 指数
		{"UK100", "UK100", 1},
		{"JP225", "JP225", 1},
		{"JAPAN", "JP225", 1},  // IC 别名
		{"HK-HSI", "HK50", 1},  // IC 别名
		{"FRANCE", "FR40", 1},  // IC 别名
		{"SPAIN", "ES35", 1},   // IC 别名
		{"USTEC", "NAS100", 1}, // NAS100 别名
		{"GER30", "GER40", 1},  // GER40 旧名
		// WP3 GER40 家族变体补种
		{"DAX40", "GER40", 1},     // DAX 40 现名
		{"DE30", "GER40", 1},      // DAX 30 旧名（合并入 GER40 家族）
		{"FDAX", "GER40", 1},      // 期货 DAX 变体
		{"GERMANY30", "GER40", 1}, // Germany 30 全名变体
		// D-067 Pepperstone # 前缀经 trimCentSuffix 剥离
		{"#UK100", "UK100", 1},
		{"#Japan225", "JP225", 1},
		{"#US30", "US30", 1},
		{"#HongKong50", "HK50", 1},
		{"#France40", "FR40", 1},
		// Cent 后缀变体
		{"EURUSDm", "EURUSD", 1},
		{"BTCUSDm", "BTCUSDT", 1},
		{"XAUUSDm", "XAUUSD", 1},
		{"AUDCADm", "AUDCAD", 1}, // Exness cent 交叉盘
		{"UK100m", "UK100", 1},   // Exness cent 指数
		// D-148 XM '#' 后缀命名变体（剥 # 后命中别名）
		{"BTCUSD#", "BTCUSDT", 1},
		{"ETHUSD#", "ETHUSDT", 1},
		{"SOLUSD#", "SOLUSDT", 1},
		{"GOLD#", "XAUUSD", 1},
		{"SILVER#", "SILVER", 1},
		{"XPTUSD#", "XPTUSD", 1},
		{"US100Cash#", "NAS100", 1},
		{"US500Cash#", "SPX500", 1},
		{"US30Cash#", "US30", 1},
		{"OILCash#", "WTI", 1},
		{"BRENTCash#", "BRENT", 1},
		{"EU50Cash#", "STOXX50", 1},
		{"SpainCash#", "ES35", 1},
		{"EURCAD#", "EURCAD", 1},
		{"MATICUSD#", "MATICUSDT", 1},
		{"ATOMUSD#", "ATOMUSDT", 1},
		// XM 误配闸：到期期货/非 USD 变体/主题篮/周末金不命中
		{"US30-DEC26", "", 0},
		{"BRENT-DEC26", "", 0},
		{"OIL-NOV26", "", 0},
		{"GOLD24-7#", "", 0},
		{"XAUEUR#", "", 0},
		{"BTCEUR#", "", 0},
		{"ETHBTC#", "", 0},
		{"AI_INDX#", "", 0},
		{"Altcoins_INDX#", "", 0},
		{"GAUUSD#", "", 0},
		// 大小写不敏感
		{"eurusd", "EURUSD", 1},
		{"gold", "XAUUSD", 1},
		{"solana", "SOLUSDT", 1},
		// 无匹配
		{"JPMm", "", 0},
		{"AAPL", "", 0},
		{"STOCK123", "", 0},
		{"GOLD365", "", 0},  // 365 期货，不收录
		{"GOLDEURO", "", 0}, // EURO 计价，不收录
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
	// 验证 alias 表覆盖全部已映射 canonical + D-067 新增
	required := []string{
		// 外汇主对
		"EURUSD", "GBPUSD", "AUDUSD", "USDJPY", "USDCHF", "USDCAD",
		"NZDUSD", "EURJPY", "GBPJPY", "EURGBP",
		// D-067 外汇交叉盘
		"AUDCAD", "AUDCHF", "AUDJPY", "AUDNZD", "AUDSGD",
		"CADCHF", "CADJPY", "CHFJPY", "CHFSGD",
		"EURAUD", "EURCAD", "EURCHF", "EURNZD", "EURSGD",
		"GBPAUD", "GBPCAD", "GBPCHF", "GBPNZD", "GBPSGD",
		"NOKSEK", "NZDCAD", "NZDCHF", "NZDJPY", "NZDSGD",
		"SGDJPY", "USDSGD",
		// 加密
		"BTCUSDT", "ETHUSDT", "TRXUSDT",
		"BNBUSDT", "SOLUSDT", "XRPUSDT", "DOGEUSDT", "LTCUSDT",
		"ADAUSDT", "AVAXUSDT", "LINKUSDT", "UNIUSDT", "DOTUSDT",
		"ATOMUSDT", "NEARUSDT", "FILUSDT", "AAVEUSDT", "XLMUSDT",
		// 贵金属
		"XAUUSD", "SILVER", "XPDUSD", "XPTUSD",
		"XCUUSD", "XZNUSD", "XPBUSD", "XALUSD", "XNIUSD",
		// 能源
		"BRENT", "WTI",
		// 指数
		"US30", "NAS100", "SPX500", "GER40",
		"UK100", "JP225", "AUS200", "HK50", "FR40", "ES35",
		"STOXX50", "US2000",
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

func TestTrimCentSuffix(t *testing.T) {
	tests := []struct {
		in, out string
	}{
		{"EURUSDm", "EURUSD"},        // MT5 cent 后缀
		{"BTCUSDm", "BTCUSD"},        // 加密 cent
		{"#UK100", "UK100"},          // Pepperstone spot 指数 # 前缀
		{"#Japan225", "Japan225"},    // Pepperstone spot 指数
		{"BTCUSD#", "BTCUSD"},        // XM # 后缀（D-148）
		{"US100Cash#", "US100Cash"},  // XM # 后缀保内文
		{"EURUSDm#", "EURUSD"},       // # 与 m 复合后缀
		{"EURUSD", "EURUSD"},         // 无后缀/前缀
		{"GOLD365", "GOLD365"},       // 365 不处理（改变品种身份）
		{"GOLDEURO", "GOLDEURO"},     // EURO 不处理
		{"US30-DEC26", "US30-DEC26"}, // 到期期货不处理
	}
	for _, tt := range tests {
		got := trimCentSuffix(tt.in)
		if got != tt.out {
			t.Errorf("trimCentSuffix(%q) = %q, want %q", tt.in, got, tt.out)
		}
	}
}

func TestCanonicalizeCrypto(t *testing.T) {
	tests := []struct {
		in, out string
	}{
		{"BTCUSD", "BTCUSDT"},
		{"ETHUSD", "ETHUSDT"},
		{"BNBUSD", "BNBUSDT"},
		{"SOLUSD", "SOLUSDT"},
		{"XRPUSD", "XRPUSDT"},
		{"AVXUSD", "AVAXUSDT"}, // FxPro 缩写
		{"LNKUSD", "LINKUSDT"}, // FxPro 缩写
		{"EURUSD", "EURUSD"},   // 非加密原样
		{"GOLD", "GOLD"},       // 非加密原样
	}
	for _, tt := range tests {
		got := Canonicalize(tt.in)
		if got != tt.out {
			t.Errorf("Canonicalize(%q) = %q, want %q", tt.in, got, tt.out)
		}
	}
}
