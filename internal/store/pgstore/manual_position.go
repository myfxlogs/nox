// manual_position.go —— D-512：fin_manual_position 手工仓位台账（只读展示域）。
// 消费边界钉死：本表只供资金全景渲染——敞口/浓度/风控/执行径零消费点（物理隔离）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// ManualPosition fin_manual_position 一行（手工录入仓位；price_usd nil=未知价如实空态）。
type ManualPosition struct {
	ID         int64
	VenueLabel string
	Symbol     string
	Qty        float64
	PriceUSD   *float64
	Note       string
	Enabled    bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ListManualPositions 启用行全量（资金全景面行源）。
func (s *Store) ListManualPositions(ctx context.Context) ([]ManualPosition, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, venue_label, symbol, qty, price_usd, note, enabled, created_at, updated_at
		FROM fin_manual_position ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list manual positions: %w", err)
	}
	defer rows.Close()
	var out []ManualPosition
	for rows.Next() {
		var r ManualPosition
		if err := rows.Scan(&r.ID, &r.VenueLabel, &r.Symbol, &r.Qty, &r.PriceUSD, &r.Note, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertManualPosition id=0 插入 / id>0 更新（venue_label 空→'manual'）。
func (s *Store) UpsertManualPosition(ctx context.Context, r *ManualPosition) error {
	if r.VenueLabel == "" {
		r.VenueLabel = "manual"
	}
	if r.Symbol == "" || r.Qty <= 0 {
		return fmt.Errorf("manual position: symbol 空或 qty<=0（拒写不编）")
	}
	if r.ID == 0 {
		return s.pool.QueryRow(ctx, `
			INSERT INTO fin_manual_position (venue_label, symbol, qty, price_usd, note, enabled)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, r.VenueLabel, r.Symbol, r.Qty, r.PriceUSD, r.Note, r.Enabled).Scan(&r.ID)
	}
	ct, err := s.pool.Exec(ctx, `
		UPDATE fin_manual_position SET venue_label=$2, symbol=$3, qty=$4, price_usd=$5, note=$6, enabled=$7, updated_at=now()
		WHERE id=$1`, r.ID, r.VenueLabel, r.Symbol, r.Qty, r.PriceUSD, r.Note, r.Enabled)
	if err != nil {
		return fmt.Errorf("update manual position %d: %w", r.ID, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("update manual position %d: not found", r.ID)
	}
	return nil
}

// DeleteManualPosition 删行（录入面撤销；台账不留软删态——行即事实）。
func (s *Store) DeleteManualPosition(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM fin_manual_position WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete manual position %d: %w", id, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("delete manual position %d: not found", id)
	}
	return nil
}
