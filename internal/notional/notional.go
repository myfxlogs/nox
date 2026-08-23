// Package notional 名义 USD 计算（D-087 §3.1）。
//
// 把「一条腿值多少 USD」变成全系统唯一答案：venue 键归一 + CS + 计价币→USD。
// 三个纯函数无外部依赖，调用方（pgstore/settlement/回填脚本）按需组合。
package notional

import "strings"

// VenueAccount 归一 venue 键：仅当后缀为 mt4/mt5 时剥去（"277842155:mt5"→"277842155"；CEX 键原样）。
// broker_symbol_map.venue 存裸账号，腿存全键 → 查 CS 前必须归一（D-087 §3.1 CS 单点修复）。
func VenueAccount(venueKey string) string {
	if strings.HasSuffix(venueKey, ":mt4") || strings.HasSuffix(venueKey, ":mt5") {
		if i := strings.LastIndex(venueKey, ":"); i > 0 {
			return venueKey[:i]
		}
	}
	return venueKey
}

// QuoteCcy 从 canonical 推计价币（D-087 §3.1）：
//   - FX 六字币对取后三（AUDJPY→JPY；EURUSD→USD）
//   - 含 USD/USDT 后缀→USD 近似（CS=1 家族既有约定）
//   - 其余→""（未知=换算不可得，调用方诚实跳过，禁止伪造）
func QuoteCcy(canonical string) string {
	if len(canonical) == 6 {
		return canonical[3:] // FX 六字币对取后三
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
