// snapshot_ccy_norm.go —— 池快照 currency 归一：历史 ”（或 NULL→”）币行是同池
// 残影（旧写径无币种列），DISTINCT ON 三键会把同一权益计两遍（敞口闸分母被稀释
// ~13pt 放行的实证病灶）。归一规则（票裁「与主币行合并取最新者」）：
// 同 (venue,venue_type) 组内 ” 行 + ≥1 命名币行 → 折并入最新命名币行——
// ” 行较新采其数值（币名保留命名者不丢维度），否则弃行；纯 ” 组原样保留。
package pgstore

import "time"

// collapseBlankCcyPool 通用折叠件：rows 已含三键最新行；gk 出 (venue,venue_type,currency)
// 键，at 出快照时点，adopt 把 src 数值面抄入 dst（” 更新时启用）。
func collapseBlankCcyPool[T any](rows []T, gk func(T) (string, string, string),
	at func(T) time.Time, adopt func(dst *T, src T)) []T {
	type key struct{ v, vt string }
	// 组内最新命名行索引 + '' 行索引。
	type grp struct {
		named int // 最新命名行索引（-1=无）
		blank int // '' 行索引（-1=无）
	}
	groups := map[key]*grp{}
	for i, r := range rows {
		v, vt, c := gk(r)
		k := key{v, vt}
		g, ok := groups[k]
		if !ok {
			g = &grp{named: -1, blank: -1}
			groups[k] = g
		}
		if c == "" {
			g.blank = i
			continue
		}
		if g.named < 0 || at(rows[i]).After(at(rows[g.named])) {
			g.named = i
		}
	}
	drop := map[int]bool{}
	for _, g := range groups {
		if g.blank < 0 || g.named < 0 {
			continue
		}
		if at(rows[g.blank]).After(at(rows[g.named])) {
			adopt(&rows[g.named], rows[g.blank]) // '' 较新：采其值入主币行
		}
		drop[g.blank] = true
	}
	if len(drop) == 0 {
		return rows
	}
	out := make([]T, 0, len(rows)-len(drop))
	for i, r := range rows {
		if !drop[i] {
			out = append(out, r)
		}
	}
	return out
}
