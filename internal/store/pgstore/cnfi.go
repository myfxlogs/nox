package pgstore

// CN-FI 境内人民币固收数据访问（D-237 R-E2：方法挂 Store，禁独立 pool/Store）。
// SSOT: docs/cnfi/CN-FI-P0.5规格包-2026-09-05-ZCode.md v0.2 §4.2；migration 0070。
// 本文件=核心序列+日历+probe run；signal/holdings/actions 各拆出独立文件保 ≤300 行。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CNFIPoint is one normalized observation destined for cnfi_series.
type CNFIPoint struct {
	AsOf    string  // 中国市场业务日 YYYY-MM-DD
	Series  string  // cgb / ig_aaa / bank_aaa（P1.5 扩：ig_aa/ncd_aaa/t2cd_aaam）
	Tenor   string  // 3m / 1y / 3y / 5y / 10y / 30y（'' = 单点序列）
	ValueBP float64 // 统一 bp（源百分比 ×100）
}

// StartCNFIRun opens a probe run row (cnfi_probe_runs).
func (s *Store) StartCNFIRun(ctx context.Context, runType string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO cnfi_probe_runs (run_type) VALUES ($1) RETURNING id`, runType).Scan(&id)
	return id, err
}

// FinishCNFIRun closes a run; errSummary is truncated by the caller to keep rows small.
func (s *Store) FinishCNFIRun(ctx context.Context, runID int64, status string, maxAsOf *time.Time, written, changed int, errSummary string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE cnfi_probe_runs SET finished_at=now(), status=$2, max_as_of=$3, rows_written=$4, rows_changed=$5, error_summary=$6 WHERE id=$1`,
		runID, status, maxAsOf, written, changed, errSummary)
	return err
}

// UpsertCNFISeries writes points idempotently (spec §1.1: 同键修订=幂等 upsert).
// sourceRef is run-scoped lineage (R2: NOT NULL — run id or "manual:<来源>:<证据>").
// Returns (written, changed): inserted rows vs value-updated rows; identical rerun = 0/0.
func (s *Store) UpsertCNFISeries(ctx context.Context, points []CNFIPoint, source, sourceRef string, fetchedAt time.Time) (int, int, error) {
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

// CNFICalendarHasDate reports whether the calendar seeds the given date (F4).
// found=false means calendar_unknown — caller must not emit staleness conclusions.
func (s *Store) CNFICalendarHasDate(ctx context.Context, day time.Time) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM cnfi_calendar WHERE cal_date = $1)`, day.Format("2006-01-02")).Scan(&ok)
	return ok, err
}

// CNFIDerivedSpread returns per-day spread series (leg1−leg2) for days where both
// legs share the same as_of, newest first, capped at limitDays distinct days (pct-v1 window source).
func (s *Store) CNFIDerivedSpread(ctx context.Context, s1, t1, s2, t2 string, limitDays int) ([]CNFIPoint, error) {
	rs, err := s.pool.Query(ctx, `
		SELECT a.as_of::text, (a.value_bp - b.value_bp)::float8
		FROM cnfi_series a
		JOIN cnfi_series b ON b.as_of = a.as_of AND b.series = $3 AND b.tenor = $4
		WHERE a.series = $1 AND a.tenor = $2
		ORDER BY a.as_of DESC LIMIT $5`, s1, t1, s2, t2, limitDays)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []CNFIPoint
	for rs.Next() {
		var day string
		var v float64
		if err := rs.Scan(&day, &v); err != nil {
			return nil, err
		}
		out = append(out, CNFIPoint{AsOf: day, ValueBP: v})
	}
	return out, rs.Err()
}

// UpsertCNFICalendarDay seeds one calendar day (public holiday data, D-047 合规).
func (s *Store) UpsertCNFICalendarDay(ctx context.Context, day time.Time, isBusiness bool, version string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cnfi_calendar (cal_date, is_business_day, cal_version)
		VALUES ($1, $2, $3)
		ON CONFLICT (cal_date) DO UPDATE
		   SET is_business_day = EXCLUDED.is_business_day, cal_version = EXCLUDED.cal_version`,
		day.Format("2006-01-02"), isBusiness, version)
	return err
}
