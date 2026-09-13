// D-292 价格提醒规则管理面 CRUD（List/Create/Update/Delete）。
// 与 notify/price_alert.go 引擎共用 price_alerts 表——管理面看到的就是引擎跑的（同表同源）。
// 引擎 30s refreshLoop 自动重载，本包不通知引擎。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// PriceAlertAdminRow 价格提醒规则管理面行（含 enabled + 全字段，区别于 notify 包的 PriceAlertRule 只取 enabled 子集）。
type PriceAlertAdminRow struct {
	ID          int64
	Symbol      string
	Op          string
	Threshold   float64
	Level       string
	Message     string
	Enabled     bool
	LastAlertAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ListPriceAlertRules 列出全部规则（含 disabled），按 enabled DESC, id ASC 排序（启用规则置顶，同状态按 id）。
func (s *Store) ListPriceAlertRules(ctx context.Context) ([]PriceAlertAdminRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, symbol, op, threshold, level, message, enabled, last_alert_at, created_at, updated_at
		FROM price_alerts
		ORDER BY enabled DESC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list price alert rules: %w", err)
	}
	defer rows.Close()
	var out []PriceAlertAdminRow
	for rows.Next() {
		var r PriceAlertAdminRow
		if err := rows.Scan(&r.ID, &r.Symbol, &r.Op, &r.Threshold, &r.Level, &r.Message, &r.Enabled, &r.LastAlertAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreatePriceAlertRule 新建规则，回填 id/created_at/updated_at。
// enabled 默认 true（DB DEFAULT）；调用方不传 enabled，新建即启用。
func (s *Store) CreatePriceAlertRule(ctx context.Context, symbol, op string, threshold float64, level, message string) (*PriceAlertAdminRow, error) {
	var r PriceAlertAdminRow
	err := s.pool.QueryRow(ctx, `
		INSERT INTO price_alerts (symbol, op, threshold, level, message)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, symbol, op, threshold, level, message, enabled, last_alert_at, created_at, updated_at
	`, symbol, op, threshold, level, message).Scan(
		&r.ID, &r.Symbol, &r.Op, &r.Threshold, &r.Level, &r.Message, &r.Enabled, &r.LastAlertAt, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create price alert rule: %w", err)
	}
	return &r, nil
}

// UpdatePriceAlertRule 全字段更新（含 enabled 启停），按 id。回填更新后行。
func (s *Store) UpdatePriceAlertRule(ctx context.Context, id int64, symbol, op string, threshold float64, level, message string, enabled bool) (*PriceAlertAdminRow, error) {
	var r PriceAlertAdminRow
	err := s.pool.QueryRow(ctx, `
		UPDATE price_alerts
		SET symbol = $2, op = $3, threshold = $4, level = $5, message = $6, enabled = $7, updated_at = now()
		WHERE id = $1
		RETURNING id, symbol, op, threshold, level, message, enabled, last_alert_at, created_at, updated_at
	`, id, symbol, op, threshold, level, message, enabled).Scan(
		&r.ID, &r.Symbol, &r.Op, &r.Threshold, &r.Level, &r.Message, &r.Enabled, &r.LastAlertAt, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("update price alert rule: %w", err)
	}
	return &r, nil
}

// DeletePriceAlertRule 硬删规则，按 id。返回是否删除了行（id 不存在=false）。
func (s *Store) DeletePriceAlertRule(ctx context.Context, id int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM price_alerts WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("delete price alert rule: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
