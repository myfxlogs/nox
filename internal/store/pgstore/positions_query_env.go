package pgstore

import (
	"context"
	"fmt"
)

// ListPositionsByEnvironment D-195-B：按 environment 过滤的 ListPositions 变体。
// environment="" 时等价于 ListPositions（不过滤）。
// 不改现有 ListPositions 签名（35+ 调用方不动）。
func (s *Store) ListPositionsByEnvironment(ctx context.Context, status, environment string, limit, offset int) ([]Position, error) {
	if limit <= 0 {
		limit = 50
	}
	if status == "ACTIVE" {
		q := `SELECT id, opportunity_id, type, status, COALESCE(expected_net_bps, 0), expected_entry_spread_bps, expected_swap_bps_daily, expected_annual_pct, realized_pnl, opened_at, closed_at, note, closing_started_at, uncertainty_reason
			FROM positions
			WHERE status IN ('OPEN','OPENING','LEGGED','CLOSING') AND ($1 = '' OR environment = $1)
			ORDER BY COALESCE(closed_at, opened_at) DESC NULLS LAST LIMIT $2 OFFSET $3`
		rows, err := s.pool.Query(ctx, q, environment, limit, offset)
		if err != nil {
			return nil, fmt.Errorf("list positions by env: %w", err)
		}
		defer rows.Close()
		return scanPositions(rows)
	}
	q := `SELECT id, opportunity_id, type, status, COALESCE(expected_net_bps, 0), expected_entry_spread_bps, expected_swap_bps_daily, expected_annual_pct, realized_pnl, opened_at, closed_at, note, closing_started_at, uncertainty_reason
		FROM positions
		WHERE ($1 = '' OR status = $1) AND ($2 = '' OR environment = $2)
		ORDER BY COALESCE(closed_at, opened_at) DESC NULLS LAST LIMIT $3 OFFSET $4`
	rows, err := s.pool.Query(ctx, q, status, environment, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list positions by env: %w", err)
	}
	defer rows.Close()
	return scanPositions(rows)
}

// ListActivePositionsByEnvironment D-195-B：按 environment 过滤的 ListActivePositions 变体。
func (s *Store) ListActivePositionsByEnvironment(ctx context.Context, environment string) ([]Position, error) {
	q := `SELECT id, opportunity_id, type, status, COALESCE(expected_net_bps, 0), expected_entry_spread_bps, expected_swap_bps_daily, expected_annual_pct, realized_pnl, opened_at, closed_at, note, notional_usd, closing_started_at, uncertainty_reason
		FROM positions
		WHERE status IN ('OPENING','OPEN','LEGGED','CLOSING') AND ($1 = '' OR environment = $1)
		ORDER BY id`
	rows, err := s.pool.Query(ctx, q, environment)
	if err != nil {
		return nil, fmt.Errorf("list active positions by env: %w", err)
	}
	defer rows.Close()
	var out []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.ID, &p.OpportunityID, &p.Type, &p.Status, &p.ExpectedNetBps,
			&p.ExpectedEntrySpreadBps, &p.ExpectedSwapBpsDaily, &p.ExpectedAnnualPct,
			&p.RealizedPnl, &p.OpenedAt, &p.ClosedAt, &p.Note, &p.NotionalUSD, &p.ClosingStartedAt, &p.UncertaintyReason); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
