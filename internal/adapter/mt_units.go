// mt_units.go D-594：报价单位归一层。scale 随 symbolMap 同源注入（broker_symbol_map
// price_scale/qty_scale），canonical=USD/lb 口径；未标定的单位敏感符号 fail-closed
// （canonical 发射抑制，原生键与 canonical 同名冲突时整帧丢弃），禁止运行时幅度自推。
package adapter

// SymbolScale 映射行单位标定（broker_symbol_map.price_scale/qty_scale）。
// PriceScale：native price × PriceScale = canonical price；QtyScale：contract_size
// 原生单位 × QtyScale = canonical 单位数/手。map 中缺席 = 未标定。
type SymbolScale struct {
	PriceScale float64
	QtyScale   float64
}

// SetSymbolScales 注入 brokerSymbol → 单位标定（仅已标定行；与 SetSymbolMap 同源，
// symbolPlan 读取面供值）。nil = 全部未标定。
func (a *MT5Adapter) SetSymbolScales(m map[string]SymbolScale) {
	cp := make(map[string]SymbolScale, len(m))
	for k, v := range m {
		cp[k] = v
	}
	a.symScales.Store(&cp)
}

// SetUnitSensitive 注入单位敏感 canonical 集（D-594：任一标定行 scale≠1 的 canonical，
// 由 dashboard 从 broker_symbol_map 派生）。敏感 canonical 的未标定行 canonical 发射抑制。
func (a *MT5Adapter) SetUnitSensitive(m map[string]bool) {
	cp := make(map[string]bool, len(m))
	for k, v := range m {
		cp[k] = v
	}
	a.unitSens.Store(&cp)
}

func (a *MT5Adapter) scaleOf(brokerSym string) (SymbolScale, bool) {
	if p := a.symScales.Load(); p != nil {
		s, ok := (*p)[brokerSym]
		return s, ok
	}
	return SymbolScale{}, false
}

func (a *MT5Adapter) unitSensitiveSet() map[string]bool {
	if p := a.unitSens.Load(); p != nil {
		return *p
	}
	return nil
}

// emitPlan D-594 发射决策：返回发射键 sym、canonical 价系数 priceScale、emit。
// 规则：emitBlocked 命中 → native 原键；敏感 canonical 未标定 → native 原键
// （若 brokerSym==canonical 名 → emit=false 整帧丢弃，宁丢勿污染）；其余 canonical 原样，
// 已标定行返回 priceScale 供 Bid/Ask 乘价，未标定非敏感 → scale=1。
func (a *MT5Adapter) emitPlan(brokerSym string) (sym string, priceScale float64, emit bool) {
	canon := a.canonical(brokerSym)
	sc, scaled := a.scaleOf(brokerSym)
	// native 键发射：emitBlocked 命中（D-383）或敏感 canonical 未标定行（D-594 同款抑制）。
	native := a.emitBlockedSet()[brokerSym] || (a.unitSensitiveSet()[canon] && !scaled)
	if native {
		if brokerSym == canon {
			return "", 0, false // 原生键=canonical 名，发射即污染 canonical 池 → 丢弃
		}
		return brokerSym, 1.0, true
	}
	if !scaled {
		sc.PriceScale = 1.0
	}
	return canon, sc.PriceScale, true
}
