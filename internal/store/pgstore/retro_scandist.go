// retro_scandist.go —— retro 通用分布扫描工具（D-180 拆出保行数闸）。
package pgstore

import (
	"context"
)

// scanDist 通用 key-count 分布扫描。
func (s *Store) scanDist(ctx context.Context, dst *map[string]int, sql string, args ...any) error {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return err
		}
		(*dst)[k] = n
	}
	return rows.Err()
}
