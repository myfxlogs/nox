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
	"DOGEUSDT": {"DOGEUSDT", "DOGUSD", "DOGEUSD", "DOGE", "DOGECOIN"},
	"LTCUSDT":  {"LTCUSDT", "LTCUSD", "LTC", "LITECOIN"},
	"ADAUSDT":  {"ADAUSDT", "ADAUSD", "ADA", "CARDANO"},
	"AVAXUSDT": {"AVAXUSDT", "AVXUSD", "AVAXUSD", "AVAX", "AVALANCHE"},
	"LINKUSDT": {"LINKUSDT", "LNKUSD", "LINKUSD", "LINK", "CHAINLINK"},
	"UNIUSDT":  {"UNIUSDT", "UNIUSD", "UNI", "UNISWAP"},
	"DOTUSDT":  {"DOTUSDT", "DOTUSD", "DOT", "POLKADOT"},
	"ATOMUSDT": {"ATOMUSDT", "ATOMUSD", "ATOM", "COSMOS"},
	"NEARUSDT": {"NEARUSDT", "NEARUSD", "NEAR"},
	"FILUSDT":  {"FILUSDT", "FILUSD", "FIL", "FILECOIN"},
	"AAVEUSDT": {"AAVEUSDT", "AAVEUSD", "AAVE"},
	"XLMUSDT":  {"XLMUSDT", "XLMUSD", "XLM", "STELLAR"},
	// D-148：XM `XxxUSD#` 加密 CFD 族（剥 # 后命中 XXXUSD 变体）——同构三段式补齐
	"1INCHUSDT": {"1INCHUSDT", "1INCHUSD", "1INCH"},
	"ALGOUSDT":  {"ALGOUSDT", "ALGOUSD", "ALGO", "ALGORAND"},
	"APEUSDT":   {"APEUSDT", "APEUSD", "APE", "APECOIN"},
	"APTUSDT":   {"APTUSDT", "APTUSD", "APT", "APTOS"},
	"ARBUSDT":   {"ARBUSDT", "ARBUSD", "ARB", "ARBITRUM"},
	"AXSUSDT":   {"AXSUSDT", "AXSUSD", "AXS"},
	"BATUSDT":   {"BATUSDT", "BATUSD", "BAT"},
	"BCHUSDT":   {"BCHUSDT", "BCHUSD", "BCH", "BITCOINCASH"},
	"BTGUSDT":   {"BTGUSDT", "BTGUSD", "BTG"},
	"CHZUSDT":   {"CHZUSDT", "CHZUSD", "CHZ", "CHILIZ"},
	"COMPUSDT":  {"COMPUSDT", "COMPUSD", "COMP", "COMPOUND"},
	"CRVUSDT":   {"CRVUSDT", "CRVUSD", "CRV"},
	"DASHUSDT":  {"DASHUSDT", "DASHUSD", "DASH"},
	"EGLDUSDT":  {"EGLDUSDT", "EGLDUSD", "EGLD"},
	"ENJUSDT":   {"ENJUSDT", "ENJUSD", "ENJ"},
	"EOSUSDT":   {"EOSUSDT", "EOSUSD", "EOS"},
	"ETCUSDT":   {"ETCUSDT", "ETCUSD", "ETC", "ETHEREUMCLASSIC"},
	"FETUSDT":   {"FETUSDT", "FETUSD", "FET"},
	"FLOWUSDT":  {"FLOWUSDT", "FLOWUSD", "FLOW"},
	"GRTUSDT":   {"GRTUSDT", "GRTUSD", "GRT", "THEGRAPH"},
	"ICPUSDT":   {"ICPUSDT", "ICPUSD", "ICP"},
	"IMXUSDT":   {"IMXUSDT", "IMXUSD", "IMX"},
	"LDOUSDT":   {"LDOUSDT", "LDOUSD", "LDO", "LIDO"},
	"LRCUSDT":   {"LRCUSDT", "LRCUSD", "LRC", "LOOPRING"},
	"MANAUSDT":  {"MANAUSDT", "MANAUSD", "MANA", "DECENTRALAND"},
	"MATICUSDT": {"MATICUSDT", "MATICUSD", "MATIC", "POLYGON"},
	"OPUSDT":    {"OPUSDT", "OPUSD", "OP", "OPTIMISM"},
	"SANDUSDT":  {"SANDUSDT", "SANDUSD", "SAND"},
	"SNXUSDT":   {"SNXUSDT", "SNXUSD", "SNX", "SYNTHETIX"},
	"STORJUSDT": {"STORJUSDT", "STORJUSD", "STORJ"},
	"STXUSDT":   {"STXUSDT", "STXUSD", "STX", "STACKS"},
	"SUSHIUSDT": {"SUSHIUSDT", "SUSHIUSD", "SUSHI"},
	"UMAUSDT":   {"UMAUSDT", "UMAUSD", "UMA"},
	"XTZUSDT":   {"XTZUSDT", "XTZUSD", "XTZ", "TEZOS"},
	"ZECUSDT":   {"ZECUSDT", "ZECUSD", "ZEC", "ZCASH"},
	"ZRXUSDT":   {"ZRXUSDT", "ZRXUSD", "ZRX"},
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
	// 能源（D-148：XM `*Cash#` 现货变体——OILCash=WTI spot、BRENTCash=BRENT spot）
	"BRENT": {"BRENT", "UKOIL", "XBRUSD", "BRENTCASH"},
	"WTI":   {"WTI", "USOIL", "XTIUSD", "OILCASH"},
	// 指数（D-067 S1.3 扩 10 品种，Pepperstone # 前缀由 trimCentSuffix 剥离；
	// D-148：XM `*Cash#` 现金指数变体——US100Cash=NAS100、US500Cash=SPX500 等）
	"US30":    {"US30", "DJ30", "WALLST30", "US30CASH"},
	"NAS100":  {"NAS100", "US100", "USATECH", "NDX100", "NASDAQ100", "USTEC", "USNDAQ100", "US100CASH"},
	"SPX500":  {"SPX500", "US500", "USA500", "SP500", "USSPX500", "US500CASH"},
	"GER40":   {"GER40", "DAX", "DAX40", "DE40", "DE30", "GDAX", "GER30", "GERMANY40", "GERMANY30", "FDAX", "GER40CASH"},
	"UK100":   {"UK100", "UK100CASH"},
	"JP225":   {"JP225", "JAPAN", "JAPAN225", "JP225CASH"},
	"AUS200":  {"AUS200", "AUS200CASH"},
	"HK50":    {"HK50", "HK-HSI", "HSI", "HONGKONG50", "HK50CASH"},
	"FR40":    {"FR40", "F40", "FRANCE", "FRANCE40", "FRA40CASH"},
	"ES35":    {"ES35", "SPAIN", "SPAIN35", "SPAINCASH"},
	"STOXX50": {"STOXX50", "EURO50", "EU50CASH"},
	"US2000":  {"US2000", "US2000CASH"},
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

// matchCanonicalDistinct 用 alias 表匹配 broker symbol → canonical，检测多命中（一个 symbol 匹配多个 canonical）。
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
