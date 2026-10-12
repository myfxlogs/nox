// exchange_dynamic.go —— D-366 S1（D-369-R3 拆分）：动态符号集（SymProvider/
// effectiveSymbols）与各采集器 syms() 方法自 exchange.go 整组移出（同包纯搬移零行为
// 变更；check-lines 超线修法）。Config.SymProvider 字段定义仍在 exchange.go（结构体
// 不可跨文件）。
package exchange

// SymbolProvider D-369 S2：动态符号集供给（radar_watchlist provider 实现；nil 安全）。
// Symbols(venue) venue 作用域——venues 列含该 venue ∪ venues 空（全所适用）。
type SymbolProvider interface {
	Symbols(venue string) []string
}

// effectiveSymbols env 基础集 ∪ provider 动态集（去重；provider nil/空集 → env 原样）。
func (c Config) effectiveSymbols(venue string) []string {
	if c.SymProvider == nil {
		return c.Symbols
	}
	extra := c.SymProvider.Symbols(venue)
	if len(extra) == 0 {
		return c.Symbols
	}
	seen := make(map[string]bool, len(c.Symbols)+len(extra))
	out := make([]string, 0, len(c.Symbols)+len(extra))
	for _, s := range c.Symbols {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range extra {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// syms D-369 S2：binance/gate/okx REST funding 采集器符号集（D-518 随类型退役删除；
// history/WS 各类型保有自有 syms()）。
