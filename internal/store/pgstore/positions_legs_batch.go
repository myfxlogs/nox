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
	q := `SELECT ` + legSelectColumns + ` FROM position_legs WHERE position_id = ANY($1) AND phase = 'OPEN' ORDER BY position_id, leg_no`
	rows, err := s.pool.Query(ctx, q, posIDs)
	if err != nil {
		return nil, fmt.Errorf("list open legs by positions: %w", err)
	}
	defer rows.Close()
	return scanLegs(rows)
}
