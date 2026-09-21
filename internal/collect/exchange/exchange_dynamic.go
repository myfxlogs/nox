// exchange_dynamic.go —— D-366 S1（D-369-R3 拆分）：动态符号集（SymProvider/
// effectiveSymbols）与各采集器 syms() 方法自 exchange.go 整组移出（同包纯搬移零行为
// 变更；check-lines 超线修法）。Config.SymProvider 字段定义仍在 exchange.go（结构体
// 不可跨文件）。
package exchange

// SymbolProvider D-369 S2：动态符号集供给（radar_watchlist provider 实现；nil 安全）。
type SymbolProvider interface {
	Symbols() []string
}

// effectiveSymbols env 基础集 ∪ provider 动态集（去重；provider nil/空集 → env 原样）。
func (c Config) effectiveSymbols() []string {
	if c.SymProvider == nil {
		return c.Symbols
	}
	extra := c.SymProvider.Symbols()
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

// syms D-369 S2：binance 采集器符号集（env ∪ provider 动态集）。
func (b *BinanceFunding) syms() []string { return b.cfg.effectiveSymbols() }

// syms D-369 S2：binance ticker 符号集。
func (b *BinanceTicker) syms() []string { return b.cfg.effectiveSymbols() }

// syms D-369 S2：gate/okx funding 符号集（env ∪ provider 动态集）。
func (g *GateFunding) syms() []string { return g.cfg.effectiveSymbols() }

// syms D-369 S2：okx funding 符号集。
func (o *OKXFunding) syms() []string { return o.cfg.effectiveSymbols() }
