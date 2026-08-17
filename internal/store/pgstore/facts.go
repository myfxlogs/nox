package pgstore

import (
	"context"
	"fmt"
	"time"

	"arb/internal/fact"

	"github.com/jackc/pgx/v5"
)

// InsertFacts 批量写入 facts（§2.2）。
func (s *Store) InsertFacts(ctx context.Context, facts []fact.Fact) error {
	if len(facts) == 0 {
		return nil
	}
	_, err := s.pool.CopyFrom(ctx,
		pgx.Identifier{"facts"},
		[]string{"kind", "venue", "symbol", "value", "unit", "ts", "src"},
		pgx.CopyFromRows(pgxCopyFromFacts(facts)))
	if err != nil {
		return fmt.Errorf("insert facts: %w", err)
	}
	return nil
}

// LatestFacts 返回每 (kind, venue, symbol) 的最新事实（§8.2 ListLatestFacts）。
func (s *Store) LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error) {
	q := `SELECT DISTINCT ON (kind, venue, symbol)
			kind, venue, symbol, value, unit, ts, src
		FROM facts
		WHERE ($1 = '' OR kind = $1)
		  AND ($2 = '' OR venue = $2)
		  AND ($3 = '' OR symbol = $3)
		ORDER BY kind, venue, symbol, ts DESC`
	rows, err := s.pool.Query(ctx, q, kind, venue, symbol)
	if err != nil {
		return nil, fmt.Errorf("latest facts: %w", err)
	}
	defer rows.Close()
	var out []fact.Fact
	for rows.Next() {
		var f fact.Fact
		if err := rows.Scan(&f.Kind, &f.Venue, &f.Symbol, &f.Value, &f.Unit, &f.Ts, &f.Src); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// QueryFacts 分页查询 facts 历史（§8.2 ListFacts）。
func (s *Store) QueryFacts(ctx context.Context, kind, venue, symbol string, limit, offset int) ([]fact.Fact, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT kind, venue, symbol, value, unit, ts, src
		FROM facts
		WHERE ($1 = '' OR kind = $1)
		  AND ($2 = '' OR venue = $2)
		  AND ($3 = '' OR symbol = $3)
		ORDER BY ts DESC
		LIMIT $4 OFFSET $5`
	rows, err := s.pool.Query(ctx, q, kind, venue, symbol, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query facts: %w", err)
	}
	defer rows.Close()
	var out []fact.Fact
	for rows.Next() {
		var f fact.Fact
		if err := rows.Scan(&f.Kind, &f.Venue, &f.Symbol, &f.Value, &f.Unit, &f.Ts, &f.Src); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// pgxCopyFromFacts 返回 pgx.CopyFromSource 的行迭代器。
func pgxCopyFromFacts(facts []fact.Fact) [][]any {
	rows := make([][]any, len(facts))
	for i, f := range facts {
		rows[i] = []any{f.Kind, f.Venue, f.Symbol, f.Value, f.Unit, f.Ts, f.Src}
	}
	return rows
}

// InsertTicks 批量写入 ticks（§2.1，TickSampler 用）。
type TickRow struct {
	Ts        time.Time
	Venue     string
	Symbol    string
	Bid       float64
	Ask       float64
	VenueType string
}

func (s *Store) InsertTicks(ctx context.Context, ticks []TickRow) error {
	if len(ticks) == 0 {
		return nil
	}
	rows := make([][]any, len(ticks))
	for i, t := range ticks {
		rows[i] = []any{t.Ts, t.Venue, t.Symbol, t.Bid, t.Ask, t.VenueType}
	}
	_, err := s.pool.CopyFrom(ctx,
		pgx.Identifier{"ticks"},
		[]string{"ts", "venue", "symbol", "bid", "ask", "venue_type"},
		pgx.CopyFromRows(rows))
	if err != nil {
		return fmt.Errorf("insert ticks: %w", err)
	}
	return nil
}

// EnsureMonthPartition 惰性创建月分区（§4.3）。
func (s *Store) EnsureMonthPartition(ctx context.Context, year int, month int) error {
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	name := fmt.Sprintf("ticks_%04d_%02d", year, month)
	q := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF ticks
		FOR VALUES FROM ('%s') TO ('%s')`,
		name, start.Format("2006-01-02"), end.Format("2006-01-02"))
	_, err := s.pool.Exec(ctx, q)
	return err
}
