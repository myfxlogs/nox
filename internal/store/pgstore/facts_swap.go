// facts_swap.go —— CFD swap facts 读面（D「CFD swap 面进研究管线」）：
// swap_long/swap_short 两 kind 最新行（每 venue×symbol 一跳）——采集在跑
// 50 万行零消费者的断档由此接上。unit=bps_daily（正=收）venue 原值原样。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// SwapFactRow 单 venue×symbol 最新 swap 行（kind=swap_long|swap_short）。
type SwapFactRow struct {
	Venue, VenueKey, Symbol, Kind string
	BpsDaily                      float64 // unit=bps_daily（正=收，负=付）
	Ts                            time.Time
}

// LatestSwapFacts 每 venue_key×symbol×kind 最新一行（fresh 窗内——
// 24h 无新行=采集死/品种停用不递陈旧值）。DISTINCT ON 跳扫。
func (s *Store) LatestSwapFacts(ctx context.Context, fresh time.Duration) ([]SwapFactRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (venue_key, symbol, kind)
		       venue, venue_key, symbol, kind, value, ts
		FROM facts
		WHERE kind IN ('swap_long','swap_short') AND ts > $1
		ORDER BY venue_key, symbol, kind, ts DESC`, time.Now().Add(-fresh))
	if err != nil {
		return nil, fmt.Errorf("latest swap facts: %w", err)
	}
	defer rows.Close()
	var out []SwapFactRow
	for rows.Next() {
		var r SwapFactRow
		if err := rows.Scan(&r.Venue, &r.VenueKey, &r.Symbol, &r.Kind, &r.BpsDaily, &r.Ts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
