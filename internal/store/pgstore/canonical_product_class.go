package pgstore

import "context"

// canonical_product_class 读面（D-558 SWAPARB 配对等价键表）。
// 背景：broker_symbol_map.product_group 是 broker 本地 MT5 目录分类（房规非产品身份，
// SG 实拍同品跨所异组致配对全灭），配对判定退役该列，等价键锚 canonical 产品类。

// ListCanonicalProductClasses 批量返回 canonical → product_class 映射。
// discoverSwapCombos 周期预取；无行=未裁定，调用方 sameProductClass fail-closed 拒。
func (s *Store) ListCanonicalProductClasses(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT canonical, product_class FROM fin_canonical_product_class`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var canonical, productClass string
		if err := rows.Scan(&canonical, &productClass); err != nil {
			return nil, err
		}
		out[canonical] = productClass
	}
	return out, rows.Err()
}
