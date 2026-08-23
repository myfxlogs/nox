package pgstore

import (
	"context"
	"fmt"
)

// GetWatchlist 返回自选 canonical 清单，按 sort 升序（D-090 §3）。
// 空表 → 空切片（合法，清单为空=不自选）。
func (s *Store) GetWatchlist(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT canonical FROM watchlist_symbols ORDER BY sort, canonical`)
	if err != nil {
		return nil, fmt.Errorf("get watchlist: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetWatchlist 全量替换自选清单（D-090 §3）。事务内 DELETE 全表 + 按传入顺序 INSERT（sort=i）。
// 幂等：重复调同入参结果一致。空入参=清空（合法）。
func (s *Store) SetWatchlist(ctx context.Context, canonicals []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("set watchlist: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) // commit 后 nil no-op
	if _, err := tx.Exec(ctx, `DELETE FROM watchlist_symbols`); err != nil {
		return fmt.Errorf("set watchlist: delete: %w", err)
	}
	for i, c := range canonicals {
		if _, err := tx.Exec(ctx,
			`INSERT INTO watchlist_symbols (canonical, sort) VALUES ($1, $2)`,
			c, i); err != nil {
			return fmt.Errorf("set watchlist: insert %q: %w", c, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("set watchlist: commit: %w", err)
	}
	return nil
}
