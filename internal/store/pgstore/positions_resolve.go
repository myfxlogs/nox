// positions_resolve.go —— D-421：UNKNOWN 腿消解的 store 读写面。
package pgstore

import (
	"context"
	"fmt"
)

// ListUnknownLegs 消解器扫描面：status='UNKNOWN' 且有查单凭据（order_id 非空）的开仓腿。
func (s *Store) ListUnknownLegs(ctx context.Context, limit int) ([]PositionLeg, error) {
	q := `SELECT ` + legSelectColumns + ` FROM position_legs
		WHERE status = 'UNKNOWN' AND order_id <> '' AND phase = 'OPEN' ORDER BY id LIMIT $1`
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list unknown legs: %w", err)
	}
	defer rows.Close()
	return scanLegs(rows)
}

// ResolveLegFill UNKNOWN 腿消解为 FILLED：回填权威价/量/费（幂等——只从 UNKNOWN 迁移）。
func (s *Store) ResolveLegFill(ctx context.Context, legID int64, price, qty, feeUSD float64) error {
	q := `UPDATE position_legs SET status = 'FILLED', price_actual = $2,
		filled_qty = $3, avg_fill_price = $2, fee_usd = NULLIF($4, 0)
		WHERE id = $1 AND status = 'UNKNOWN'`
	_, err := s.pool.Exec(ctx, q, legID, price, qty, feeUSD)
	return err
}

// ResolveLegFail UNKNOWN 腿消解为 FAILED（venue 确认未成交：canceled/rejected 且零成交）。
func (s *Store) ResolveLegFail(ctx context.Context, legID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE position_legs SET status = 'FAILED' WHERE id = $1 AND status = 'UNKNOWN'`, legID)
	return err
}
