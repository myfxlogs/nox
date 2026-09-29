// retro_depth.go —— D-578：cex_depth_sampler 深度采样读面（sysretro 三所深度章节原料）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// DepthFact D-578：cex_depth_sampler 深度采样行（三所 spot spread/±0.5% 深度）。
type DepthFact struct {
	Venue  string // venue_key（如 binance:spot）
	Symbol string
	Kind   string
	Value  float64
	Ts     time.Time
}

// DepthFacts 读 [since,until) 窗内 src=cex_depth_sampler 全部采样行（ts 升序，报告聚合原料）。
func (s *Store) DepthFacts(ctx context.Context, since, until time.Time) ([]DepthFact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT venue_key, COALESCE(symbol,''), kind, value, ts FROM facts
		WHERE src = 'cex_depth_sampler' AND ts >= $1 AND ts < $2 ORDER BY ts`, since, until)
	if err != nil {
		return nil, fmt.Errorf("depth facts: %w", err)
	}
	defer rows.Close()
	var out []DepthFact
	for rows.Next() {
		var d DepthFact
		if err := rows.Scan(&d.Venue, &d.Symbol, &d.Kind, &d.Value, &d.Ts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
