// radar_watchlist.go —— D-369 S2：确认信号 watchlist 读写（radar 写侧 upsert，
// core 读侧 provider/seed）。仅写不删（last_seen 刷新；v1 无过期清理）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// RadarWatchlistRow radar_watchlist 行。
type RadarWatchlistRow struct {
	Symbol    string
	Venues    []string
	Reason    string
	FirstSeen time.Time
	LastSeen  time.Time
}

// UpsertRadarWatchlist 写/刷新（同 symbol 更新 venues/reason/last_seen；first_seen 不动）。
func (s *Store) UpsertRadarWatchlist(ctx context.Context, symbol string, venues []string, reason string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO radar_watchlist (symbol, venues, reason)
		VALUES ($1, $2, $3)
		ON CONFLICT (symbol) DO UPDATE
		SET venues = EXCLUDED.venues, reason = EXCLUDED.reason, last_seen = now()`,
		symbol, venues, reason)
	if err != nil {
		return fmt.Errorf("upsert radar watchlist %s: %w", symbol, err)
	}
	return nil
}

// LoadRadarWatchlist 全表读出（KV 量级小）。
func (s *Store) LoadRadarWatchlist(ctx context.Context) ([]RadarWatchlistRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT symbol, venues, reason, first_seen, last_seen FROM radar_watchlist ORDER BY symbol`)
	if err != nil {
		return nil, fmt.Errorf("load radar watchlist: %w", err)
	}
	defer rows.Close()
	var out []RadarWatchlistRow
	for rows.Next() {
		var r RadarWatchlistRow
		if err := rows.Scan(&r.Symbol, &r.Venues, &r.Reason, &r.FirstSeen, &r.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
