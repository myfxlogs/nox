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
// ①：消解转 FILLED 也是终态腿产生点——回读腿补 leg:<id> commission 行（幂等）。
func (s *Store) ResolveLegFill(ctx context.Context, legID int64, price, qty, feeUSD float64) error {
	q := `UPDATE position_legs SET status = 'FILLED', price_actual = $2,
		filled_qty = $3, avg_fill_price = $2, fee_usd = NULLIF($4::float8, 0)
		WHERE id = $1 AND status = 'UNKNOWN'`
	if _, err := s.pool.Exec(ctx, q, legID, price, qty, feeUSD); err != nil {
		return err
	}
	leg, err := s.legByID(ctx, legID)
	if err != nil || leg == nil {
		return err // 腿不存在与 UPDATE 未命中同语义：不补行不报错
	}
	return s.EnsureLegCommissionCashflow(ctx, *leg, nil)
}

// legByID 按主键读腿（D-013 R1 ① ResolveLegFill 回读用）。
func (s *Store) legByID(ctx context.Context, legID int64) (*PositionLeg, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+legSelectColumns+` FROM position_legs WHERE id = $1`, legID)
	if err != nil {
		return nil, fmt.Errorf("leg by id %d: %w", legID, err)
	}
	defer rows.Close()
	legs, err := scanLegs(rows)
	if err != nil || len(legs) == 0 {
		return nil, err
	}
	return &legs[0], nil
}

// ResolveLegFail UNKNOWN 腿消解为 FAILED（venue 确认未成交：canceled/rejected 且零成交）。
func (s *Store) ResolveLegFail(ctx context.Context, legID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE position_legs SET status = 'FAILED' WHERE id = $1 AND status = 'UNKNOWN'`, legID)
	return err
}
