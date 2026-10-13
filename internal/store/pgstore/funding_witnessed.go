// funding_witnessed.go —— D-280 方案 A：分所 funding 见证符号读面（深度采集
// 动态基名源；自 facts.go 拆出保行数闸）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// ListFundingWitnessedSymbols D-280 方案 A：分所精集——venues 各所近 window 内
// funding 事实见证的 symbol 集（候选腿真实集合=有 funding 证据的符号才值得采深度）。
// 返回原始 symbol（BTCUSDT 等），基名归一在调用侧（carryDepthBase 单源）。
func (s *Store) ListFundingWitnessedSymbols(ctx context.Context, venue string, window time.Duration) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT symbol FROM facts WHERE kind='funding' AND venue=$1 AND ts>=$2 ORDER BY 1`,
		venue, time.Now().Add(-window))
	if err != nil {
		return nil, fmt.Errorf("list funding witnessed symbols: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var sym string
		if err := rows.Scan(&sym); err != nil {
			return nil, err
		}
		out = append(out, sym)
	}
	return out, rows.Err()
}
