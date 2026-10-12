// cooled_exits.go —— D-372：冷却窗平仓行窄查询（gate 统一止损重进冷却取数）。
// 独立窄投影而非扩 Position 结构体（Position 无 ExitKind 字段，零结构体改动）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// CooledExit 冷却窗内平仓行（冷却判定所需三列）。
type CooledExit struct {
	OpportunityID string
	ExitKind      string
	ClosedAt      *time.Time
}

// ListCooledExits 返回 kinds 内平仓且 closed_at > since 的行。
// kind+窗口过滤推进 SQL（exit_kind=NULL/空串与窗口外行天然排除），应用层只剩组合键匹配。
func (s *Store) ListCooledExits(ctx context.Context, kinds []string, since time.Time) ([]CooledExit, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT opportunity_id, exit_kind, closed_at FROM positions
		WHERE status='CLOSED' AND exit_kind = ANY($1) AND closed_at > $2`, kinds, since)
	if err != nil {
		return nil, fmt.Errorf("list cooled exits: %w", err)
	}
	defer rows.Close()
	var out []CooledExit
	for rows.Next() {
		var ce CooledExit
		if err := rows.Scan(&ce.OpportunityID, &ce.ExitKind, &ce.ClosedAt); err != nil {
			return nil, fmt.Errorf("scan cooled exit: %w", err)
		}
		out = append(out, ce)
	}
	return out, rows.Err()
}
