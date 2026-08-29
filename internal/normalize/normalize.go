// Package normalize 实现 broker 符号归一规则库（D-140 S1）。
// 规则库只产出 canonical 归一，绝不推 contract_size——CS 必须来自 broker_symbol_map 真实行
// （MT5 采集或人工录入），无 CS=CS 门拦截（D-134 语义不放宽一毫米）。
//
// 查找链（调用方按序）：
//  1. exact broker_symbol_map 命中 → 返回 canonical
//  2. 未命中 → 规则库匹配 → 返回 canonical（CS 仍走原查询=查不到 fail-closed）
//  3. 仍未命中 → 现行为不变（unknown）
//
// 种子规则从现网 285 行映射 + alias_table.go 归纳扩充，覆盖油类/FX/贵金属/加密/指数。
package normalize

import (
	"regexp"
	"sort"
	"strings"
)

// Rule 归一规则：regex 模式 → canonical，含优先级。
// 优先级数值小先匹配（允许高优先规则覆盖宽泛规则）。
type Rule struct {
	Pattern   *regexp.Regexp
	Canonical string
	Priority  int
	Desc      string // 人类可读描述（审计用）
}

// RuleEngine 归一规则引擎（编译后的规则集，线程安全只读）。
type RuleEngine struct {
	rules []Rule // 按 Priority 排序
}

// seedRules 种子规则（从施工提示词示例 + alias_table.go 归纳）。
// 规则只产 canonical，不产 CS。大小写不敏感（(?i)）。
var seedRules = []Rule{
	// 能源（高优先：精确前缀优先于宽泛）
	{regexp.MustCompile(`(?i)^(xbr|ukoil|brent)`), "BRENT", 10, "布伦特原油"},
	{regexp.MustCompile(`(?i)^(wti|usoil|uscrud|cl)`), "WTI", 10, "WTI 原油"},
	// 贵金属
	{regexp.MustCompile(`(?i)^(xau|gold)`), "XAUUSD", 10, "黄金"},
	{regexp.MustCompile(`(?i)^(xag|silver)`), "SILVER", 10, "白银"},
	{regexp.MustCompile(`(?i)^(xpd|palladium)`), "XPDUSD", 10, "钯金"},
	{regexp.MustCompile(`(?i)^(xpt|platinum)`), "XPTUSD", 10, "铂金"},
	{regexp.MustCompile(`(?i)^(xcu|copper)`), "XCUUSD", 10, "铜"},
	// 外汇直盘+交叉盘（7 直盘 + 交叉盘，匹配 6 字母 FX 对）
	{regexp.MustCompile(`(?i)^(eurusd|eur\.usd|eurusd\..*)$`), "EURUSD", 20, "欧元美元"},
	{regexp.MustCompile(`(?i)^(gbpusd|gbp\.usd)$`), "GBPUSD", 20, "英镑美元"},
	{regexp.MustCompile(`(?i)^(audusd|aud\.usd)$`), "AUDUSD", 20, "澳元美元"},
	{regexp.MustCompile(`(?i)^(nzdusd|nzd\.usd)$`), "NZDUSD", 20, "纽元美元"},
	{regexp.MustCompile(`(?i)^(usdjpy|usd\.jpy)$`), "USDJPY", 20, "美元日元"},
	{regexp.MustCompile(`(?i)^(usdchf|usd\.chf)$`), "USDCHF", 20, "美元瑞郎"},
	{regexp.MustCompile(`(?i)^(usdcad|usd\.cad)$`), "USDCAD", 20, "美元加元"},
	{regexp.MustCompile(`(?i)^(eurjpy|eur\.jpy)$`), "EURJPY", 20, "欧元日元"},
	{regexp.MustCompile(`(?i)^(gbpjpy|gbp\.jpy)$`), "GBPJPY", 20, "英镑日元"},
	{regexp.MustCompile(`(?i)^(audjpy|aud\.jpy)$`), "AUDJPY", 20, "澳元日元"},
	{regexp.MustCompile(`(?i)^(eurgbp|eur\.gbp)$`), "EURGBP", 20, "欧元英镑"},
	{regexp.MustCompile(`(?i)^(audcad|aud\.cad)$`), "AUDCAD", 20, "澳元加元"},
	{regexp.MustCompile(`(?i)^(audchf|aud\.chf)$`), "AUDCHF", 20, "澳元瑞郎"},
	{regexp.MustCompile(`(?i)^(audnzd|aud\.nzd)$`), "AUDNZD", 20, "澳元纽元"},
	{regexp.MustCompile(`(?i)^(audsgd|aud\.sgd)$`), "AUDSGD", 20, "澳元新元"},
	{regexp.MustCompile(`(?i)^(cadchf|cad\.chf)$`), "CADCHF", 20, "加元瑞郎"},
	{regexp.MustCompile(`(?i)^(cadjpy|cad\.jpy)$`), "CADJPY", 20, "加元日元"},
	{regexp.MustCompile(`(?i)^(chfjpy|chf\.jpy)$`), "CHFJPY", 20, "瑞郎日元"},
	{regexp.MustCompile(`(?i)^(chfsgd|chf\.sgd)$`), "CHFSGD", 20, "瑞郎新元"},
	{regexp.MustCompile(`(?i)^(euraud|eur\.aud)$`), "EURAUD", 20, "欧元澳元"},
	{regexp.MustCompile(`(?i)^(eurcad|eur\.cad)$`), "EURCAD", 20, "欧元加元"},
	{regexp.MustCompile(`(?i)^(eurchf|eur\.chf)$`), "EURCHF", 20, "欧元瑞郎"},
	{regexp.MustCompile(`(?i)^(eurnzd|eur\.nzd)$`), "EURNZD", 20, "欧元纽元"},
	{regexp.MustCompile(`(?i)^(eursgd|eur\.sgd)$`), "EURSGD", 20, "欧元新元"},
	{regexp.MustCompile(`(?i)^(gbpaud|gbp\.aud)$`), "GBPAUD", 20, "英镑澳元"},
	{regexp.MustCompile(`(?i)^(gbpcad|gbp\.cad)$`), "GBPCAD", 20, "英镑加元"},
	{regexp.MustCompile(`(?i)^(gbpchf|gbp\.chf)$`), "GBPCHF", 20, "英镑瑞郎"},
	{regexp.MustCompile(`(?i)^(gbpnzd|gbp\.nzd)$`), "GBPNZD", 20, "英镑纽元"},
	{regexp.MustCompile(`(?i)^(gbpsgd|gbp\.sgd)$`), "GBPSGD", 20, "英镑新元"},
	{regexp.MustCompile(`(?i)^(noksek|nok\.sek)$`), "NOKSEK", 20, "挪威瑞典克朗"},
	{regexp.MustCompile(`(?i)^(nzdcad|nzd\.cad)$`), "NZDCAD", 20, "纽元加元"},
	{regexp.MustCompile(`(?i)^(nzdchf|nzd\.chf)$`), "NZDCHF", 20, "纽元瑞郎"},
	{regexp.MustCompile(`(?i)^(nzdjpy|nzd\.jpy)$`), "NZDJPY", 20, "纽元日元"},
	{regexp.MustCompile(`(?i)^(nzdsgd|nzd\.sgd)$`), "NZDSGD", 20, "纽元新元"},
	{regexp.MustCompile(`(?i)^(sgdjpy|sgd\.jpy)$`), "SGDJPY", 20, "新元日元"},
	{regexp.MustCompile(`(?i)^(usdsgd|usd\.sgd)$`), "USDSGD", 20, "美元新元"},
	// 加密 CFD（BTCUSD → BTCUSDT 归一，D-056）
	{regexp.MustCompile(`(?i)^(btcusd|btc|bitcoin)$`), "BTCUSDT", 30, "比特币"},
	{regexp.MustCompile(`(?i)^(ethusd|eth|ethereum)$`), "ETHUSDT", 30, "以太坊"},
	{regexp.MustCompile(`(?i)^(trxusd|trx|tron)$`), "TRXUSDT", 30, "波场"},
	{regexp.MustCompile(`(?i)^(bnbusd|bnb|binancecoin)$`), "BNBUSDT", 30, "币安币"},
	{regexp.MustCompile(`(?i)^(solusd|sol|solana)$`), "SOLUSDT", 30, "索拉纳"},
	{regexp.MustCompile(`(?i)^(xrpusd|xrp|ripple)$`), "XRPUSDT", 30, "瑞波"},
	{regexp.MustCompile(`(?i)^(dogeusd|doge|dogecoin)$`), "DOGEUSDT", 30, "狗狗币"},
	{regexp.MustCompile(`(?i)^(ltcusd|ltc|litecoin)$`), "LTCUSDT", 30, "莱特币"},
	// CEX 直通：*USDT → 原样（已有约定，最低优先级兜底）
	{regexp.MustCompile(`(?i)^(.*usdt)$`), "", 90, "CEX USDT 直通（canonical=原符号大写）"},
	// 指数
	{regexp.MustCompile(`(?i)^(us30|dj30|wallst30)`), "US30", 40, "道琼斯"},
	{regexp.MustCompile(`(?i)^(nas100|us100|usatech|ndx100|nasdaq100|ustec|usndaq100)`), "NAS100", 40, "纳斯达克"},
	{regexp.MustCompile(`(?i)^(spx500|us500|usa500|sp500|usspx500)`), "SPX500", 40, "标普500"},
	{regexp.MustCompile(`(?i)^(ger40|dax|dax40|de40|de30|gdax|ger30|germany40|germany30|fdax)`), "GER40", 40, "德国DAX"},
	{regexp.MustCompile(`(?i)^(uk100)$`), "UK100", 40, "英国富时"},
	{regexp.MustCompile(`(?i)^(jp225|japan|japan225)`), "JP225", 40, "日经225"},
	{regexp.MustCompile(`(?i)^(aus200)$`), "AUS200", 40, "澳洲200"},
	{regexp.MustCompile(`(?i)^(hk50|hk-hsi|hsi|hongkong50)`), "HK50", 40, "恒生"},
	{regexp.MustCompile(`(?i)^(fr40|f40|france|france40)`), "FR40", 40, "法国CAC"},
	{regexp.MustCompile(`(?i)^(es35|spain|spain35)`), "ES35", 40, "西班牙IBEX"},
	{regexp.MustCompile(`(?i)^(stoxx50|euro50)`), "STOXX50", 40, "欧洲斯托克"},
	{regexp.MustCompile(`(?i)^(us2000)$`), "US2000", 40, "罗素2000"},
}

// NewRuleEngine 创建规则引擎（编译种子规则，按优先级排序）。
func NewRuleEngine() *RuleEngine {
	rules := make([]Rule, len(seedRules))
	copy(rules, seedRules)
	sort.SliceStable(rules, func(i, j int) bool {
		return rules[i].Priority < rules[j].Priority
	})
	return &RuleEngine{rules: rules}
}

// Match 用规则库匹配 broker symbol → canonical。
// 返回 canonical + 是否命中。规则只产 canonical，不产 CS。
// CEX USDT 直通规则（Canonical=""）返回原符号大写。
func (e *RuleEngine) Match(brokerSymbol string) (string, bool) {
	for _, r := range e.rules {
		if r.Pattern.MatchString(brokerSymbol) {
			if r.Canonical == "" {
				// CEX 直通：canonical = 原符号大写
				return strings.ToUpper(brokerSymbol), true
			}
			return r.Canonical, true
		}
	}
	return "", false
}

// Rules 返回规则列表（审计/调试用，只读）。
func (e *RuleEngine) Rules() []Rule {
	return e.rules
}
