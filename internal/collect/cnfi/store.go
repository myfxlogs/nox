package cnfi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store writes normalized points into cnfi_* tables (spec §4.2; migration 0070).
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// StartRun opens a probe run row (cnfi_probe_runs); finish with FinishRun.
func (s *Store) StartRun(ctx context.Context, runType string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO cnfi_probe_runs (run_type) VALUES ($1) RETURNING id`, runType).Scan(&id)
	return id, err
}

// FinishRun closes a run; errSummary is truncated by the caller to keep rows small.
func (s *Store) FinishRun(ctx context.Context, runID int64, status string, maxAsOf *time.Time, written, changed int, errSummary string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE cnfi_probe_runs SET finished_at=now(), status=$2, max_as_of=$3, rows_written=$4, rows_changed=$5, error_summary=$6 WHERE id=$1`,
		runID, status, maxAsOf, written, changed, errSummary)
	return err
}

// UpsertSeries writes points idempotently (spec §1.1: 同键修订=幂等 upsert).
// sourceRef is the run-scoped lineage (R2: NOT NULL — "manual:<来源>:<证据>" or run id).
// Returns (written, changed): inserted rows vs value-updated rows.
func (s *Store) UpsertSeries(ctx context.Context, points []Point, source, sourceRef string, fetchedAt time.Time) (int, int, error) {
	var written, changed int
	for _, p := range points {
		if p.AsOf == "" || p.Series == "" || p.Tenor == "" {
			return written, changed, fmt.Errorf("cnfi upsert: empty key in point %+v", p)
		}
		var inserted bool
		err := s.pool.QueryRow(ctx, `
			INSERT INTO cnfi_series (as_of, series, tenor, value_bp, source, source_ref, fetched_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (as_of, series, tenor) DO UPDATE
			   SET value_bp = EXCLUDED.value_bp,
			       source = EXCLUDED.source,
			       source_ref = EXCLUDED.source_ref,
			       fetched_at = EXCLUDED.fetched_at
			 WHERE cnfi_series.value_bp IS DISTINCT FROM EXCLUDED.value_bp
			RETURNING (xmax = 0) AS inserted`,
			p.AsOf, p.Series, p.Tenor, p.ValueBP, source, sourceRef, fetchedAt).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) {
			// 冲突但同值（WHERE 过滤）→ 无变化，幂等重跑不报错
			continue
		}
		if err != nil {
			return written, changed, fmt.Errorf("cnfi upsert %s %s %s: %w", p.AsOf, p.Series, p.Tenor, err)
		}
		if inserted {
			written++
		} else {
			changed++
		}
	}
	return written, changed, nil
}

// CalendarHasDate reports whether the calendar seeds the given date (F4).
// found=false means calendar_unknown — caller must not emit staleness conclusions.
func (s *Store) CalendarHasDate(ctx context.Context, day time.Time) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM cnfi_calendar WHERE cal_date = $1)`, day.Format("2006-01-02")).Scan(&ok)
	return ok, err
}
