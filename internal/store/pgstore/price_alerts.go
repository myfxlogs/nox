// D-282 价格提醒规则存取。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// PriceAlertRule 对应 price_alerts 表。
type PriceAlertRule struct {
	ID          int64
	Symbol      string
	Op          string
	Threshold   float64
	Level       string
	Message     string
	LastAlertAt *time.Time
}

// ListEnabledPriceAlerts 返回所有 enabled=true 的规则。
func (s *Store) ListEnabledPriceAlerts(ctx context.Context) ([]PriceAlertRule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, symbol, op, threshold, level, message, last_alert_at
		FROM price_alerts WHERE enabled = true ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("list price alerts: %w", err)
	}
	defer rows.Close()
	var out []PriceAlertRule
	for rows.Next() {
		var r PriceAlertRule
		if err := rows.Scan(&r.ID, &r.Symbol, &r.Op, &r.Threshold, &r.Level, &r.Message, &r.LastAlertAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdatePriceAlertLastAlert 更新上次告警时间（节流持久化，重启不重置）。
func (s *Store) UpdatePriceAlertLastAlert(ctx context.Context, id int64, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE price_alerts SET last_alert_at = $1 WHERE id = $2`, at, id)
	return err
}
