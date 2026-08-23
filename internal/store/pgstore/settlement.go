package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SettlementRow position_settlements 行（D-087 §3.2）。
type SettlementRow struct {
	PositionID    int64
	Vkey          string
	ExpectedUsd   float64
	RealizedUsd   float64
	NotionalUsd   float64
	HoldDays      float64
	SkippedReason string
	SettledAt     time.Time
}

// GetPosition 读单条持仓（D-087 settlement.Settle 用）。不存在→nil, nil。
func (s *Store) GetPosition(ctx context.Context, id int64) (*Position, error) {
	q := `SELECT id, opportunity_id, type, status, expected_net_bps, expected_entry_spread_bps, expected_swap_bps_daily, expected_annual_pct, realized_pnl, opened_at, closed_at, note
		FROM positions WHERE id = $1`
	var p Position
	err := s.pool.QueryRow(ctx, q, id).Scan(&p.ID, &p.OpportunityID, &p.Type, &p.Status, &p.ExpectedNetBps,
		&p.ExpectedEntrySpreadBps, &p.ExpectedSwapBpsDaily, &p.ExpectedAnnualPct,
		&p.RealizedPnl, &p.OpenedAt, &p.ClosedAt, &p.Note)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get position %d: %w", id, err)
	}
	return &p, nil
}

// InsertSettlement 写 position_settlements 行（D-087 §3.2，幂等 ON CONFLICT DO NOTHING）。
func (s *Store) InsertSettlement(ctx context.Context, r *SettlementRow) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, notional_usd, hold_days, skipped_reason, settled_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (position_id) DO NOTHING
	`, r.PositionID, r.Vkey, r.ExpectedUsd, r.RealizedUsd, r.NotionalUsd, r.HoldDays, r.SkippedReason, r.SettledAt)
	if err != nil {
		return fmt.Errorf("insert settlement pos=%d: %w", r.PositionID, err)
	}
	return nil
}

// HasSettlement 查 position_settlements 是否已有行（D-087 幂等短路）。
func (s *Store) HasSettlement(ctx context.Context, posID int64) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM position_settlements WHERE position_id = $1`, posID).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("check settlement pos=%d: %w", posID, err)
}
