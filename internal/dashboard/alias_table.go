package dashboard

// canonicalAliases canonical → 候选名/变体（全部大写比较）。
// 从 4 个 broker 实证数据归纳 + D-042/D-056/D-060 经验沉淀（D-066）。
// AutoMapBroker RPC + ListMappingSuggestions + symseed 工具共用此表（单一真相源，P3）。
//
// 匹配规则：broker_symbol 大写精确匹配 alias，或 trimCentSuffix 后匹配。
// 合约规格由 CanonicalContractProfile 众数校验（D-042/D-061），此处只管名字。
var canonicalAliases = map[string][]string{
	// 外汇主对（10）
	"EURUSD": {"EURUSD"},
	"GBPUSD": {"GBPUSD"},
	"AUDUSD": {"AUDUSD"},
	"USDJPY": {"USDJPY"},
	"USDCHF": {"USDCHF"},
	"USDCAD": {"USDCAD"},
	"NZDUSD": {"NZDUSD"},
	"EURJPY": {"EURJPY"},
	"GBPJPY": {"GBPJPY"},
	"EURGBP": {"EURGBP"},
	// 加密（CFD BTCUSD → BTCUSDT 归一，D-056）
	"BTCUSDT": {"BTCUSD", "BTCUSDT", "BTC"},
	"ETHUSDT": {"ETHUSD", "ETHUSDT", "ETH"},
	"TRXUSDT": {"TRXUSD", "TRXUSDT", "TRON"},
	// 贵金属
	"XAUUSD": {"XAUUSD", "GOLD"},
	"SILVER": {"XAGUSD", "SILVER"},
	// 能源
	"BRENT": {"BRENT", "UKOIL", "XBRUSD"},
	"WTI":   {"WTI", "USOIL", "XTIUSD"},
	// 指数
	"US30":   {"US30", "DJ30", "WALLST30"},
	"NAS100": {"NAS100", "US100", "USATECH", "NDX100", "NASDAQ100"},
	"SPX500": {"SPX500", "US500", "USA500", "SP500"},
	"GER40":  {"GER40", "DAX", "DE40", "GDAX"},
}

// allCanonicals 返回 alias 表中全部 canonical（排序），供 ListCanonicals 等消费。
func allCanonicals() []string {
	out := make([]string, 0, len(canonicalAliases))
	for c := range canonicalAliases {
		out = append(out, c)
	}
	sortStrings(out)
	return out
}

// matchCanonical 用 alias 表匹配 broker symbol → canonical。
// 先精确匹配，再 trimCentSuffix 后匹配。返回 canonical + 是否命中。
func matchCanonical(brokerSymbol string) (string, bool) {
	up := upper(brokerSymbol)
	// 精确匹配
	for canon, aliases := range canonicalAliases {
		for _, al := range aliases {
			if up == al {
				return canon, true
			}
		}
	}
	// Cent 后缀变体
	upTrim := upper(trimCentSuffix(brokerSymbol))
	if upTrim != up {
		for canon, aliases := range canonicalAliases {
			for _, al := range aliases {
				if upTrim == al {
					return canon, true
				}
			}
		}
	}
	return "", false
}

// matchCanonicalDistinct 同 matchCanonical，但检测多命中（一个 symbol 匹配多个 canonical）。
// 返回 canonical + 命中数。hits>1 = multi_hit。
func matchCanonicalDistinct(brokerSymbol string) (canon string, hits int) {
	up := upper(brokerSymbol)
	seen := map[string]bool{}
	check := func(s string) {
		for c, aliases := range canonicalAliases {
			for _, al := range aliases {
				if s == al && !seen[c] {
					seen[c] = true
					canon = c
					hits++
				}
			}
		}
	}
	check(up)
	upTrim := upper(trimCentSuffix(brokerSymbol))
	if upTrim != up {
		check(upTrim)
	}
	return canon, hits
}

func upper(s string) string {
	r := []byte(s)
	for i, c := range r {
		if c >= 'a' && c <= 'z' {
			r[i] = c - 32
		}
	}
	return string(r)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
