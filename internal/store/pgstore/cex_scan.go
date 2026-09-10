// D-271 CEX 只读扫描器 Store：理财 APR + 跨所价差 CRUD。
package pgstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CexEarnAPR fin_cex_earn_apr 行。
type CexEarnAPR struct {
	ID          int64
	Venue       string
	Ccy         string
	ProductType string
	APR         float64
	TierAPR     sql.NullString
	Ts          time.Time
}

// CexSpread fin_cex_spread 行。
type CexSpread struct {
	ID        int64
	Symbol    string
	VenueBuy  string
	VenueSell string
	PriceBuy  float64
	PriceSell float64
	SpreadBps float64
	Ts        time.Time
}

// InsertCexEarnAPR 插入理财 APR 行，回填 id + ts。
func (s *Store) InsertCexEarnAPR(ctx context.Context, r *CexEarnAPR) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO fin_cex_earn_apr (venue, ccy, product_type, apr, tier_apr)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, ts
	`, r.Venue, r.Ccy, r.ProductType, r.APR, r.TierAPR).Scan(&r.ID, &r.Ts)
	if err != nil {
		return fmt.Errorf("insert cex earn apr: %w", err)
	}
	return nil
}

// LatestCexEarnAPRs 每所每币种最新一行（DISTINCT ON venue, ccy）。
func (s *Store) LatestCexEarnAPRs(ctx context.Context) ([]CexEarnAPR, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (venue, ccy)
			id, venue, ccy, product_type, apr, tier_apr, ts
		FROM fin_cex_earn_apr
		ORDER BY venue, ccy, ts DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("latest cex earn aprs: %w", err)
	}
	defer rows.Close()
	var out []CexEarnAPR
	for rows.Next() {
		var r CexEarnAPR
		if err := rows.Scan(&r.ID, &r.Venue, &r.Ccy, &r.ProductType, &r.APR, &r.TierAPR, &r.Ts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertCexSpread 插入跨所价差行，回填 id + ts。
func (s *Store) InsertCexSpread(ctx context.Context, r *CexSpread) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO fin_cex_spread (symbol, venue_buy, venue_sell, price_buy, price_sell, spread_bps)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, ts
	`, r.Symbol, r.VenueBuy, r.VenueSell, r.PriceBuy, r.PriceSell, r.SpreadBps).Scan(&r.ID, &r.Ts)
	if err != nil {
		return fmt.Errorf("insert cex spread: %w", err)
	}
	return nil
}

// LatestCexSpreads 每 symbol 最新一行（DISTINCT ON symbol）。
func (s *Store) LatestCexSpreads(ctx context.Context) ([]CexSpread, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (symbol)
			id, symbol, venue_buy, venue_sell, price_buy, price_sell, spread_bps, ts
		FROM fin_cex_spread
		ORDER BY symbol, ts DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("latest cex spreads: %w", err)
	}
	defer rows.Close()
	var out []CexSpread
	for rows.Next() {
		var r CexSpread
		if err := rows.Scan(&r.ID, &r.Symbol, &r.VenueBuy, &r.VenueSell, &r.PriceBuy, &r.PriceSell, &r.SpreadBps, &r.Ts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
