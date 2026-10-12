// blackout.go —— D-053 ③ 执行禁区币种集纯函数（D-061 ②起 selfwatch 就绪表共用；
// 迁自 autotrade/gate_macro_blackout.go——分层：禁区域知识归财历包，autotrade/
// selfwatch 两消费方向下依赖本包无环）。
package macro

import "strings"

// isoCurrencies ISO 4217 币种/贵金属码集（canonical 拆半判定用；XAU/XAG=ISO 4217 贵金属段）。
// 与财历币种域（fetch_mql5 countryIDToCurrency）对齐 + 贵金属。
var isoCurrencies = map[string]bool{
	"USD": true, "EUR": true, "JPY": true, "GBP": true, "AUD": true,
	"CAD": true, "CHF": true, "NZD": true, "CNY": true, "HKD": true,
	"SGD": true, "KRW": true, "SEK": true, "NOK": true, "MXN": true,
	"XAU": true, "XAG": true,
}

// BlackoutCurrencySet 禁区币种集纯函数（D-053 R1④ Gate/Engine/selfwatch 共用件）：
// 配置基集（逗号分隔，空=USD 缺省）∪ canonical 6 字母两半 ISO 推导（两半皆 ISO 才并入——
// EURUSD/XAUUSD 命中；BTCUSDT 7 字母、SOLUSDT 非 ISO 半不推导）。ANY 匹配序不敏感。
func BlackoutCurrencySet(base, canonical string) []string {
	if strings.TrimSpace(base) == "" {
		base = "USD"
	}
	set := map[string]bool{}
	for _, c := range strings.Split(base, ",") {
		if c = strings.ToUpper(strings.TrimSpace(c)); c != "" {
			set[c] = true
		}
	}
	if len(canonical) == 6 {
		a, b := strings.ToUpper(canonical[:3]), strings.ToUpper(canonical[3:])
		if isoCurrencies[a] && isoCurrencies[b] {
			set[a], set[b] = true, true
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	return out
}
