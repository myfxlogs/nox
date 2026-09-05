package pgstore

// CN-FI 境内人民币固收数据访问（D-237 R-E2：方法挂 Store，禁独立 pool/Store）。
// SSOT: docs/cnfi/CN-FI-P0.5规格包-2026-09-05-ZCode.md v0.2 §4.2；migration 0070。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// UpsertCNFISignalState persists signal evaluation output (observe-only 期 observed_state 恒 observe，C11).
func (s *Store) UpsertCNFISignalState(ctx context.Context, key, observedState string, lastValue float64, sampleN int, observedAsOf, evidenceRef string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cnfi_signal_state (key, observed_state, last_value, sample_n, observed_as_of, updated_at, evidence_ref)
		VALUES ($1, $2, $3, $4, $5, now(), $6)
		ON CONFLICT (key) DO UPDATE
		   SET observed_state = EXCLUDED.observed_state,
		       last_value = EXCLUDED.last_value,
		       sample_n = EXCLUDED.sample_n,
		       observed_as_of = EXCLUDED.observed_as_of,
		       updated_at = now(),
		       evidence_ref = EXCLUDED.evidence_ref`,
		key, observedState, lastValue, sampleN, observedAsOf, evidenceRef)
	return err
}

// UpsertCNFIInstrument registers or updates an instrument (P3; codes are public, not personal data).
func (s *Store) UpsertCNFIInstrument(ctx context.Context, code, kind, name string, isActive bool) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cnfi_instruments (code, kind, name, is_active)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (code) DO UPDATE SET kind = EXCLUDED.kind, name = EXCLUDED.name, is_active = EXCLUDED.is_active`,
		code, kind, name, isActive)
	return err
}

// UpsertCNFINAV stores one fund NAV day (eastmoney T+1; best-effort source).
func (s *Store) UpsertCNFINAV(ctx context.Context, code, navDate string, nav float64, source string, fetchedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cnfi_nav (code, nav_date, nav, source, fetched_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (code, nav_date) DO UPDATE
		   SET nav = EXCLUDED.nav, source = EXCLUDED.source, fetched_at = EXCLUDED.fetched_at
		 WHERE cnfi_nav.nav IS DISTINCT FROM EXCLUDED.nav`,
		code, navDate, nav, source, fetchedAt)
	return err
}

// LatestCNFINAVDate returns the most recent nav_date for a code (” if none).
func (s *Store) LatestCNFINAVDate(ctx context.Context, code string) (string, error) {
	var d string
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(max(nav_date)::text,'') FROM cnfi_nav WHERE code=$1`, code).Scan(&d)
	return d, err
}

// AddCNFIHolding records one manual L2 entry (P3; amounts stay in PG runtime, D-047).
func (s *Store) AddCNFIHolding(ctx context.Context, code, layer string, qty, costValue float64, openedDate, note string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO cnfi_holdings (code, layer, qty, cost_value, opened_date, note)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		code, layer, qty, costValue, openedDate, note).Scan(&id)
	return id, err
}

// CNFIHoldingRow is one holding joined with its latest NAV valuation.
type CNFIHoldingRow struct {
	ID          int64
	Code        string
	Layer       string
	Qty         float64
	CostValue   float64
	OpenedDate  string
	LatestNAV   float64
	NAVAsOf     string
	MarketValue float64
}

// ListCNFIHoldingsValued joins holdings with their latest NAV (missing NAV → market value 0, flag via NAVAsOf=”).
func (s *Store) ListCNFIHoldingsValued(ctx context.Context) ([]CNFIHoldingRow, error) {
	rs, err := s.pool.Query(ctx, `
		SELECT h.id, h.code, h.layer, h.qty::float8, h.cost_value::float8, h.opened_date::text,
		       COALESCE(n.nav::float8, 0), COALESCE(n.nav_date::text, ''),
		       h.qty::float8 * COALESCE(n.nav::float8, 0)
		FROM cnfi_holdings h
		LEFT JOIN LATERAL (
		  SELECT nav, nav_date FROM cnfi_nav WHERE code = h.code ORDER BY nav_date DESC LIMIT 1
		) n ON true
		ORDER BY h.layer, h.id`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []CNFIHoldingRow
	for rs.Next() {
		var r CNFIHoldingRow
		if err := rs.Scan(&r.ID, &r.Code, &r.Layer, &r.Qty, &r.CostValue, &r.OpenedDate,
			&r.LatestNAV, &r.NAVAsOf, &r.MarketValue); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// CNFILayerTargets reads the four-layer target ratio config (key='layer_targets').
// Returns layer→pct; missing config = empty map (caller renders nothing rather than guessing).
func (s *Store) CNFILayerTargets(ctx context.Context) (map[string]float64, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT value::text FROM cnfi_signal_config WHERE key='layer_targets'`).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return map[string]float64{}, nil
		}
		return nil, err
	}
	var m map[string]float64
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("cnfi layer_targets json: %w", err)
	}
	return m, nil
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

// UpsertCNFISignalConfigRaw writes a signal_config value (研究席位经部署流程使用).
func (s *Store) UpsertCNFISignalConfigRaw(ctx context.Context, key string, value json.RawMessage, updatedBy, evidenceRef string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cnfi_signal_config (key, value, updated_at, updated_by, evidence_ref)
		VALUES ($1, $2, now(), $3, $4)
		ON CONFLICT (key) DO UPDATE
		   SET value = EXCLUDED.value, updated_at = now(),
		       updated_by = EXCLUDED.updated_by, evidence_ref = EXCLUDED.evidence_ref`,
		key, value, updatedBy, evidenceRef)
	return err
}

// AddCNFIAction records one operational action (投资笔记 D-242; amounts runtime-only).
func (s *Store) AddCNFIAction(ctx context.Context, code, layer, actionType string, qty, amountCny float64, navAtAction *float64, signalSnapshot, rationale, actionDate string) (int64, error) {
	if (actionType == "buy" || actionType == "sell") && strings.TrimSpace(rationale) == "" {
		return 0, fmt.Errorf("cnfi action %s: rationale 必填（投资笔记铁律）", actionType)
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO cnfi_actions (code, layer, action_type, qty, amount_cny, nav_at_action, signal_snapshot, rationale, action_date)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		code, layer, actionType, qty, amountCny, navAtAction, signalSnapshot, rationale, actionDate).Scan(&id)
	return id, err
}

// ListCNFIActions returns the newest actions (owner-only personal data; UI 展示合法，禁入 git).
func (s *Store) ListCNFIActions(ctx context.Context, limit int) ([]CNFIActionRow, error) {
	rs, err := s.pool.Query(ctx, `
		SELECT id, code, layer, action_type, qty::float8, amount_cny::float8,
		       COALESCE(signal_snapshot::text,''), rationale, action_date::text
		FROM cnfi_actions ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []CNFIActionRow
	for rs.Next() {
		var r CNFIActionRow
		if err := rs.Scan(&r.ID, &r.Code, &r.Layer, &r.ActionType, &r.Qty, &r.AmountCny,
			&r.SignalSnapshot, &r.Rationale, &r.ActionDate); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// CNFISignalSnapshotJSON reads all signal states as a JSON snapshot (行动时点证据).
func (s *Store) CNFISignalSnapshotJSON(ctx context.Context) (json.RawMessage, error) {
	var raw json.RawMessage
	rows, err := s.pool.Query(ctx,
		`SELECT COALESCE(jsonb_agg(jsonb_build_object('key',key,'state',observed_state,'value',last_value,'sample_n',sample_n,'as_of',observed_as_of))::text,'[]')
		 FROM cnfi_signal_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
	}
	return raw, rows.Err()
}

// CNFIActionRow is one operational action (投资笔记).
type CNFIActionRow struct {
	ID             int64
	Code           string
	Layer          string
	ActionType     string
	Qty            float64
	AmountCny      float64
	SignalSnapshot string
	Rationale      string
	ActionDate     string
}

// CNFISignalConfigJSON reads one signal_config value as raw json (” key → nil).
func (s *Store) CNFISignalConfigJSON(ctx context.Context, key string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := s.pool.QueryRow(ctx,
		`SELECT value::text FROM cnfi_signal_config WHERE key=$1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}
