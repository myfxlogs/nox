package pgstore

import (
	"context"
	"fmt"
)

// ListOpenLegsByPositionIDs 批量返回多个持仓的 OPEN 腿（D-132 T1 venue 敞口门用）。
// 复用 position_legs_position_idx 索引（position_id = ANY 走索引），勿建新索引。
func (s *Store) ListOpenLegsByPositionIDs(ctx context.Context, posIDs []int64) ([]PositionLeg, error) {
	if len(posIDs) == 0 {
		return nil, nil
	}
	q := `SELECT id, position_id, leg_no, phase, venue, account, symbol, side, qty,
		price_expected, price_actual, order_id, status, ts
		FROM position_legs WHERE position_id = ANY($1) AND phase = 'OPEN' ORDER BY position_id, leg_no`
	rows, err := s.pool.Query(ctx, q, posIDs)
	if err != nil {
		return nil, fmt.Errorf("list open legs by positions: %w", err)
	}
	defer rows.Close()
	var out []PositionLeg
	for rows.Next() {
		var l PositionLeg
		if err := rows.Scan(&l.ID, &l.PositionID, &l.LegNo, &l.Phase, &l.Venue, &l.Account,
			&l.Symbol, &l.Side, &l.Qty, &l.PriceExpected, &l.PriceActual, &l.OrderID, &l.Status, &l.Ts); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
