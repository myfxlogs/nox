// Package notional 名义 USD 计算（D-087 §3.1）。
//
// 把「一条腿值多少 USD」变成全系统唯一答案：venue 键归一 + CS + 计价币→USD。
// 三个纯函数无外部依赖，调用方（pgstore/settlement/回填脚本）按需组合。
package notional

import (
	"nox/internal/data/keydomain"
	"strings"
)

// VenueAccount 归一 venue 键：仅当后缀为 mt4/mt5 时剥去（"277842155:mt5"→"277842155"；CEX 键原样）。
// broker_symbol_map.venue 存裸账号，腿存全键 → 查 CS 前必须归一（D-087 §3.1 CS 单点修复）。
func VenueAccount(venueKey string) string {
	if keydomain.IsMTKey(venueKey) {
		return keydomain.Parse(venueKey).Venue
	}
	return venueKey
}

// knownQuoteCcies 已知计价币集（七直盘 + 交叉盘计价侧）。
var knownQuoteCcies = map[string]bool{
	"USD": true, "JPY": true, "GBP": true, "EUR": true,
	"AUD": true, "NZD": true, "CAD": true, "CHF": true,
}

// isAlpha 判断字符串是否全字母（A-Z，大写）。
func isAlpha(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return len(s) > 0
}

// QuoteCcy 从 canonical 推计价币（D-087 §3.1，返修包②修正）：
//   - len==6 且前后两段均全字母且后段 ∈ 已知计价币集 → 返回后段
//     （交叉盘 AUDJPY→JPY、GBPAUD→AUD；直盘 EURUSD→USD）
//   - 否则 HasSuffix USD/USDT → "USD"（XAUUSD/BTCUSDT）
//   - 否则 ""（=USD 假定，fx=1；如 NAS100/SPX500/US30/UKOIL/BRENT 等指数/商品 CFD）
//
// 已知限制（记注释，不修）：UK100 等非 USD 计价指数在 "" 假定下 notional 不换算——
// 待未来 broker_symbol_map 加 quote_ccy 列，本次不扩。
func QuoteCcy(canonical string) string {
	if len(canonical) == 6 {
		base, quote := canonical[:3], canonical[3:]
		if isAlpha(base) && isAlpha(quote) && knownQuoteCcies[quote] {
			return quote
		}
	}
	if strings.HasSuffix(canonical, "USD") || strings.HasSuffix(canonical, "USDT") {
		return "USD"
	}
	return ""
}

// LegUSD 单腿名义 USD = qty × cs × price × fx（fx=1 当 quote=USD）。
// 任一因子 ≤0 → 0（诚实缺省，防伪造）。
func LegUSD(qty, cs, price, fx float64) float64 {
	if qty <= 0 || cs <= 0 || price <= 0 || fx <= 0 {
		return 0
	}
	return qty * cs * price * fx
}

// FXPair D-633：计价币→七直盘对+换算方向单源（pgstore.USDPer/usdnotional.USDPerNow 共用）。
// 返回 (直盘 symbol, invert=true 时 rate=1/px 〔USD-base〕, ok=false=非七直盘/USD)。
func FXPair(quoteCcy string) (pair string, invert bool, ok bool) {
	switch quoteCcy {
	case "JPY", "CHF", "CAD":
		return "USD" + quoteCcy, true, true
	case "EUR", "GBP", "AUD", "NZD":
		return quoteCcy + "USD", false, true
	}
	return "", false, false
}
