package pgstore

import "context"

// D-558 续段：SWAPARB 发现径符号归一化读面。
// facts.symbol 存 broker 本地符号（D-325 双发：canonical 标签行 + native variant 行，
// 如 XAUUSDm/ADAUSD#），配对等式与类查必须先归一到 canonical。
// 归一化键=DB 映射（broker_symbol_map 实读），禁尾缀规则进配对判定（票面禁线）。

// venueSymbolKey (venue, symbol) 复合键。
type venueSymbolKey struct {
	venue  string
	symbol string
}

// SymbolCanonMap 符号→canonical 批量归一化表（discoverSwapCombos 周期预取）。
// nil 表 Resolve 全 false = fail-closed（查询失败面禁配对）。
type SymbolCanonMap struct {
	byNative    map[venueSymbolKey]string // broker_symbol 行：原生名→canonical
	byCanonical map[venueSymbolKey]bool   // canonical 名行：该 venue 有映射到此 canonical 的行
}

// LoadSymbolCanonMap broker_symbol_map 全量倾载（数百行级，discover 周期调用零压）。
func (s *Store) LoadSymbolCanonMap(ctx context.Context) (*SymbolCanonMap, error) {
	rows, err := s.pool.Query(ctx, `SELECT venue, broker_symbol, canonical FROM broker_symbol_map`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := &SymbolCanonMap{
		byNative:    make(map[venueSymbolKey]string),
		byCanonical: make(map[venueSymbolKey]bool),
	}
	for rows.Next() {
		var venue, brokerSym, canonical string
		if err := rows.Scan(&venue, &brokerSym, &canonical); err != nil {
			return nil, err
		}
		m.byNative[venueSymbolKey{venue, brokerSym}] = canonical
		m.byCanonical[venueSymbolKey{venue, canonical}] = true
	}
	return m, rows.Err()
}

// Resolve facts 符号→canonical。两段：
// ① (venue, symbol)=broker_symbol 精确行→该行 canonical（native variant 与 native==canonical 同行）；
// ② symbol 即 canonical（该 venue 存在映射到它的行——D-325 canonical 标签行）→ 原样返回。
// 都未命中=false：真未映射，调用方 fail-closed 跳过+留痕促补映射。
func (m *SymbolCanonMap) Resolve(venue, symbol string) (string, bool) {
	if m == nil {
		return "", false
	}
	if c, ok := m.byNative[venueSymbolKey{venue, symbol}]; ok {
		return c, true
	}
	if m.byCanonical[venueSymbolKey{venue, symbol}] {
		return symbol, true
	}
	return "", false
}
