// CN-FI 持仓 + NAV + instrument 访问（从 cnfi.go 拆出，保 cnfi.go ≤300 行）。
package pgstore

import (
	"context"
	"time"
)

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
// D-247 S2：ljjz=累计净值（收益计算基准；nil=源未披露，行仍入）。
func (s *Store) UpsertCNFINAV(ctx context.Context, code, navDate string, nav float64, ljjz *float64, source string, fetchedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cnfi_nav (code, nav_date, nav, ljjz, source, fetched_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (code, nav_date) DO UPDATE
		   SET nav = EXCLUDED.nav, ljjz = EXCLUDED.ljjz,
		       source = EXCLUDED.source, fetched_at = EXCLUDED.fetched_at
		 WHERE cnfi_nav.nav IS DISTINCT FROM EXCLUDED.nav
		    OR cnfi_nav.ljjz IS DISTINCT FROM EXCLUDED.ljjz`,
		code, navDate, nav, ljjz, source, fetchedAt)
	return err
}

// CNFILJJZRow is one dated accumulated-NAV point (non-nil ljjz only).
type CNFILJJZRow struct {
	Date string
	LJJZ float64
}

// LastTwoCNFILJJZ D-247 S3：最近两个非空 LJJZ 点（日采增量断点检测：新点 vs 前一非空点，首点不判）。
func (s *Store) LastTwoCNFILJJZ(ctx context.Context, code string) ([]CNFILJJZRow, error) {
	rs, err := s.pool.Query(ctx, `
		SELECT nav_date::text, ljjz::float8 FROM cnfi_nav
		WHERE code=$1 AND ljjz IS NOT NULL ORDER BY nav_date DESC LIMIT 2`, code)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []CNFILJJZRow
	for rs.Next() {
		var r CNFILJJZRow
		if err := rs.Scan(&r.Date, &r.LJJZ); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// SetCNFIInstrumentBreak D-247 S3/R1：LJJZ 口径断点日（取最早断点日写入）。
func (s *Store) SetCNFIInstrumentBreak(ctx context.Context, code string, day time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE cnfi_instruments SET caliber_break_date=$2 WHERE code=$1
		 AND (caliber_break_date IS NULL OR caliber_break_date > $2)`, code, day)
	return err
}

// CNFICandidate is one pooled candidate fund (layer non-null).
type CNFICandidate struct {
	Code      string
	Name      string
	Layer     string
	BreakDate *time.Time
}

// ListCNFICandidates D-247 S3/S4：候选池（layer 非空，kind=fund，active）。
func (s *Store) ListCNFICandidates(ctx context.Context) ([]CNFICandidate, error) {
	rs, err := s.pool.Query(ctx, `
		SELECT code, name, layer, caliber_break_date FROM cnfi_instruments
		WHERE kind='fund' AND is_active AND layer IS NOT NULL ORDER BY layer, code`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []CNFICandidate
	for rs.Next() {
		var c CNFICandidate
		if err := rs.Scan(&c.Code, &c.Name, &c.Layer, &c.BreakDate); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rs.Err()
}

// CNFINAVSeries D-247 S4：单基金非空 LJJZ 序列（升序）。
func (s *Store) CNFINAVSeries(ctx context.Context, code string) ([]CNFILJJZRow, error) {
	rs, err := s.pool.Query(ctx, `
		SELECT nav_date::text, ljjz::float8 FROM cnfi_nav
		WHERE code=$1 AND ljjz IS NOT NULL ORDER BY nav_date ASC`, code)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []CNFILJJZRow
	for rs.Next() {
		var r CNFILJJZRow
		if err := rs.Scan(&r.Date, &r.LJJZ); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
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
