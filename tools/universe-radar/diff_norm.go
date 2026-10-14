package main

// diff_norm.go —— 符号归一/别名/年化纯函数件（原 diff.go 拆分，零行为变）。

import "strings"

// normBase 剥报价/交割后缀得 base：
//
//	perp：XAUUSDT→XAU、XAU_USDT→XAU、XAU-USDT-SWAP→XAU；
//	交割：BTC-USDT-241227→BTC（okx 六位数日期）、BTCUSDT_240927→BTC（binance 下划线日期）、
//	BTC_USDT_20241227→BTC（gate 八位数日期）——日期尾段先剥再走 perp 链。
func normBase(symbol string) string {
	s := symbol
	// 交割日期尾段先剥（-YYMMDD/_YYMMDD/_YYYYMMDD，≥6 位纯数字——
	// _USDT/_USD_UM 等定价币尾含非数字不误伤）；okx USD_UM 形剥完日期剩
	// -USD_UM 定价币尾，交下方后缀链（-USD_UM 先于 -USD 判，防截成 -UM）。
	s = stripDigitsTail(s)
	for _, suf := range []string{"-USDT-SWAP", "-USD_UM", "_USD_UM", "-USDT", "-USDC", "-USD",
		"_USDT", "_USDC", "_USD", "USDT", "USDC"} {
		if strings.HasSuffix(s, suf) {
			return strings.TrimSuffix(s, suf)
		}
	}
	return s
}

// stripDigitsTail 剥「-_ 分隔的 ≥6 位纯数字尾段」（交割日期）。剥一次返回新串。
func stripDigitsTail(s string) string {
	if i := strings.LastIndexAny(s, "-_"); i > 0 && allDigits(s[i+1:]) && len(s[i+1:]) >= 6 {
		return s[:i]
	}
	return s
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// alias base→canonical 别名表（设计附录 A，初版静态 map；追加属配置活）。
var alias = map[string]string{
	"XAU": "XAUUSD", "XAG": "XAGUSD", "XPT": "XPTUSD", "XPD": "XPDUSD",
	"COPPER": "XCUUSD", "CL": "WTI", "BZ": "BRENT", "NATGAS": "NATGAS",
}

// canonicalOf base→canonical：别名表命中→映射值；未命中→base 本身（加密货币照常）。
func canonicalOf(base string) string {
	if c, ok := alias[base]; ok {
		return c
	}
	return base
}

// annualize funding 年化 %：rate × (24/interval_h) × 365 × 100；interval 逐符号真值，
// 禁默认 8h（D-301a 同族）。intervalH<=0=无数据 → nil。
func annualize(rate float64, intervalH int) *float64 {
	if intervalH <= 0 {
		return nil
	}
	v := rate * (24.0 / float64(intervalH)) * 365 * 100
	return &v
}
