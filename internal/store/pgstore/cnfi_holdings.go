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
