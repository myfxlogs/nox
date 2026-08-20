package dashboard

// canonicalAliases canonical → 候选名/变体（全部大写比较）。
// 从 4 个 broker 实证数据归纳 + D-042/D-056/D-060/D-066/D-067 经验沉淀。
// AutoMapBroker RPC + ListMappingSuggestions + symseed 工具共用此表（单一真相源，P3）。
//
// 匹配规则：broker_symbol 经 Canonicalize 归一后大写精确匹配 alias，或 trimCentSuffix 后匹配。
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
	// 外汇交叉盘（D-067 S1.1，4 broker 实证 3-4 broker 都有）
	"AUDCAD": {"AUDCAD"},
	"AUDCHF": {"AUDCHF"},
	"AUDJPY": {"AUDJPY"},
	"AUDNZD": {"AUDNZD"},
	"AUDSGD": {"AUDSGD"},
	"CADCHF": {"CADCHF"},
	"CADJPY": {"CADJPY"},
	"CHFJPY": {"CHFJPY"},
	"CHFSGD": {"CHFSGD"},
	"EURAUD": {"EURAUD"},
	"EURCAD": {"EURCAD"},
	"EURCHF": {"EURCHF"},
	"EURNZD": {"EURNZD"},
	"EURSGD": {"EURSGD"},
	"GBPAUD": {"GBPAUD"},
	"GBPCAD": {"GBPCAD"},
	"GBPCHF": {"GBPCHF"},
	"GBPNZD": {"GBPNZD"},
	"GBPSGD": {"GBPSGD"},
	"NOKSEK": {"NOKSEK"},
	"NZDCAD": {"NZDCAD"},
	"NZDCHF": {"NZDCHF"},
	"NZDJPY": {"NZDJPY"},
	"NZDSGD": {"NZDSGD"},
	"SGDJPY": {"SGDJPY"},
	"USDSGD": {"USDSGD"},
	// 加密（CFD BTCUSD → BTCUSDT 归一，D-056；D-067 S1.2 扩 15 品种）
	"BTCUSDT":  {"BTCUSD", "BTCUSDT", "BTC", "BITCOIN"},
	"ETHUSDT":  {"ETHUSD", "ETHUSDT", "ETH", "ETHEREUM"},
	"TRXUSDT":  {"TRXUSD", "TRXUSDT", "TRON"},
	"BNBUSDT":  {"BNBUSDT", "BNBUSD", "BNB", "BINANCECOIN"},
	"SOLUSDT":  {"SOLUSDT", "SOLUSD", "SOL", "SOLANA"},
	"XRPUSDT":  {"XRPUSDT", "XRPUSD", "XRP", "RIPPLE"},
	"DOGEUSDT": {"DOGEUSDT", "DOGUSD", "DOGE", "DOGECOIN"},
	"LTCUSDT":  {"LTCUSDT", "LTCUSD", "LTC", "LITECOIN"},
	"ADAUSDT":  {"ADAUSDT", "ADAUSD", "ADA", "CARDANO"},
	"AVAXUSDT": {"AVAXUSDT", "AVXUSD", "AVAX", "AVALANCHE"},
	"LINKUSDT": {"LINKUSDT", "LNKUSD", "LINK", "CHAINLINK"},
	"UNIUSDT":  {"UNIUSDT", "UNIUSD", "UNI", "UNISWAP"},
	"DOTUSDT":  {"DOTUSDT", "DOTUSD", "DOT", "POLKADOT"},
	"ATOMUSDT": {"ATOMUSDT", "ATOMUSD", "ATOM", "COSMOS"},
	"NEARUSDT": {"NEARUSDT", "NEARUSD", "NEAR"},
	"FILUSDT":  {"FILUSDT", "FILUSD", "FIL", "FILECOIN"},
	"AAVEUSDT": {"AAVEUSDT", "AAVEUSD", "AAVE"},
	"XLMUSDT":  {"XLMUSDT", "XLMUSD", "XLM", "STELLAR"},
	// 贵金属（D-067 S1.4 扩 7 品种，Exness X-代码 ↔ 全名）
	"XAUUSD": {"XAUUSD", "GOLD"},
	"SILVER": {"XAGUSD", "SILVER"},
	"XPDUSD": {"XPDUSD", "PALLADIUM"},
	"XPTUSD": {"XPTUSD", "PLATINUM"},
	"XCUUSD": {"XCUUSD", "COPPER"},
	"XZNUSD": {"XZNUSD", "ZINC"},
	"XPBUSD": {"XPBUSD", "LEAD"},
	"XALUSD": {"XALUSD", "ALUMINIUM"},
	"XNIUSD": {"XNIUSD", "NICKEL"},
	// 能源
	"BRENT": {"BRENT", "UKOIL", "XBRUSD"},
	"WTI":   {"WTI", "USOIL", "XTIUSD"},
	// 指数（D-067 S1.3 扩 10 品种，Pepperstone # 前缀由 trimCentSuffix 剥离）
	"US30":    {"US30", "DJ30", "WALLST30"},
	"NAS100":  {"NAS100", "US100", "USATECH", "NDX100", "NASDAQ100", "USTEC", "USNDAQ100"},
	"SPX500":  {"SPX500", "US500", "USA500", "SP500", "USSPX500"},
	"GER40":   {"GER40", "DAX", "DE40", "GDAX", "GER30", "GERMANY40"},
	"UK100":   {"UK100"},
	"JP225":   {"JP225", "JAPAN", "JAPAN225"},
	"AUS200":  {"AUS200"},
	"HK50":    {"HK50", "HK-HSI", "HSI", "HONGKONG50"},
	"FR40":    {"FR40", "F40", "FRANCE", "FRANCE40"},
	"ES35":    {"ES35", "SPAIN", "SPAIN35"},
	"STOXX50": {"STOXX50", "EURO50"},
	"US2000":  {"US2000"},
	"DE30":    {"DE30"},
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

// matchCanonical 用 alias 表匹配 broker symbol → canonical（D-067 修复：先经 Canonicalize 归一）。
// 先精确匹配，再 trimCentSuffix 后匹配。返回 canonical + 是否命中。
func matchCanonical(brokerSymbol string) (string, bool) {
	up := upper(Canonicalize(brokerSymbol))
	// 精确匹配
	for canon, aliases := range canonicalAliases {
		for _, al := range aliases {
			if up == al {
				return canon, true
			}
		}
	}
	// Cent 后缀 / # 前缀变体
	upTrim := upper(Canonicalize(trimCentSuffix(brokerSymbol)))
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
	up := upper(Canonicalize(brokerSymbol))
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
	upTrim := upper(Canonicalize(trimCentSuffix(brokerSymbol)))
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
