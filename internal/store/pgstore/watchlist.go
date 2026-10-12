package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// WatchlistItem D-090 返修包① R1：自选清单双维项（canonical + venues）。
// Venues nil/空 = 全部源（语义「全部」显式，DB 存 NULL）；非空 = 仅该 venueKey 集合。
type WatchlistItem struct {
	Canonical string
	Venues    []string // nil/空 = 全部源
}

// GetWatchlist 返回自选清单，按 sort 升序（D-090 §3 + 返修包① R1 双维）。
// 空表 → 空切片（合法，清单为空=不自选）。venues NULL → nil（全部源语义）。
func (s *Store) GetWatchlist(ctx context.Context) ([]WatchlistItem, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT canonical, venues FROM watchlist_symbols ORDER BY sort, canonical`)
	if err != nil {
		return nil, fmt.Errorf("get watchlist: %w", err)
	}
	defer rows.Close()
	var out []WatchlistItem
	for rows.Next() {
		var c string
		var venues pgtype.Array[string]
		if err := rows.Scan(&c, &venues); err != nil {
			return nil, fmt.Errorf("scan watchlist row: %w", err)
		}
		item := WatchlistItem{Canonical: c}
		if venues.Valid && len(venues.Elements) > 0 {
			item.Venues = append([]string(nil), venues.Elements...)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// SetWatchlist 全量替换自选清单（D-090 §3 + 返修包① R1 双维）。事务内 DELETE 全表 +
// 按传入顺序 INSERT（sort=i，venues 空=存 NULL=全部源语义）。幂等：重复调同入参结果一致。
// 空入参=清空（合法）。
func (s *Store) SetWatchlist(ctx context.Context, items []WatchlistItem) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("set watchlist: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) // commit 后 nil no-op
	if _, err := tx.Exec(ctx, `DELETE FROM watchlist_symbols`); err != nil {
		return fmt.Errorf("set watchlist: delete: %w", err)
	}
	for i, item := range items {
		// venues 空 → 存 NULL（语义「全部」显式，与 Get 的 nil=全部对称）
		var venuesArg any
		if len(item.Venues) > 0 {
			venuesArg = item.Venues
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO watchlist_symbols (canonical, sort, venues) VALUES ($1, $2, $3)`,
			item.Canonical, i, venuesArg); err != nil {
			return fmt.Errorf("set watchlist: insert %q: %w", item.Canonical, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("set watchlist: commit: %w", err)
	}
	return nil
}
