// allow_rules.go D-443 S1：autotrade_allow_rules 加载器（venue/canonical 单表双 kind）。
// 加载失败 → 调用方 startup fatal（fail-closed 同 xasset 先例）；venue 空集=全拒（最保守），
// canonical 空集=全放行（D-122 语义不动）。simulated 闸对齐 environment（live 只读 false）。
package pgstore

import (
	"context"
	"fmt"
)

// LoadAutotradeAllowRules 按 kind 分流两 map（ venues/canonicals 均非 nil——空集合法态）。
func (s *Store) LoadAutotradeAllowRules(ctx context.Context, simulated bool) (venues, canonicals map[string]bool, err error) {
	rows, err := s.pool.Query(ctx, `
		SELECT kind, value FROM autotrade_allow_rules
		WHERE enabled AND simulated = $1`, simulated)
	if err != nil {
		return nil, nil, fmt.Errorf("load autotrade allow rules: %w", err)
	}
	defer rows.Close()
	venues, canonicals = map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			return nil, nil, err
		}
		switch kind {
		case "venue":
			venues[value] = true
		case "canonical":
			canonicals[value] = true
		}
	}
	return venues, canonicals, rows.Err()
}
