// personal_positions（个人持仓，财务线 TRX 持仓接入 D-046）存储。
// 非 broker/CEX 双腿模型：质押仓/对冲空单，独立轻量表（设计文档 §3）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// PersonalPosition 对应 personal_positions 表（设计文档 §3.2）。
// WarnHigh/CritHigh 为 nil = 该级别不设（此持仓仅展示不报警）。
type PersonalPosition struct {
	ID          int64
	Asset       string
	Kind        string
	PriceSymbol string
	WarnHigh    *float64
	CritHigh    *float64
	Note        string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ClosedAt    *time.Time
}

// ListPersonalPositions 分页返回个人持仓（status 空 = 全部）。
func (s *Store) ListPersonalPositions(ctx context.Context, status string, limit, offset int) ([]PersonalPosition, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, asset, kind, price_symbol, warn_high, crit_high, note, status, created_at, updated_at, closed_at
		FROM personal_positions
		WHERE ($1 = '' OR status = $1)
		ORDER BY id ASC LIMIT $2 OFFSET $3`
	rows, err := s.pool.Query(ctx, q, status, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list personal positions: %w", err)
	}
	defer rows.Close()
	var out []PersonalPosition
	for rows.Next() {
		var p PersonalPosition
		if err := rows.Scan(&p.ID, &p.Asset, &p.Kind, &p.PriceSymbol, &p.WarnHigh, &p.CritHigh,
			&p.Note, &p.Status, &p.CreatedAt, &p.UpdatedAt, &p.ClosedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListOpenPersonalPositions 返回全部 OPEN 个人持仓（供 PersonalMonitor 加载）。
func (s *Store) ListOpenPersonalPositions(ctx context.Context) ([]PersonalPosition, error) {
	return s.ListPersonalPositions(ctx, "OPEN", 200, 0)
}
