// proposal_go_trace.go —— 待授权面语义分层（10-22 业主实证：proposed 单态混
// 两义——已 go 尝试被条件闸拒的「武装待资」行 vs 未裁决行同显）。联查源=
// go 审计行（alerts「提案执行审计 #N …」格式文案，dciexec.goAudit 写径）。
package pgstore

import (
	"context"
	"fmt"
)

// ProposalGoAttempted 提案→go 尝试痕集（armed 待资标记位）：审计行 pattern
// 「提案执行审计 #<id> 」命中 id 集。proposed 行有痕=go 过闸拒=条件武装
// （条件到自动跑）；executed/declined 终态行不进调用面（status 已翻）。
func (s *Store) ProposalGoAttempted(ctx context.Context, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT substring(message from '提案执行审计 #([0-9]+)')::bigint AS pid
		FROM alerts
		WHERE message LIKE '提案执行审计 #%'
		  AND substring(message from '提案执行审计 #([0-9]+)')::bigint = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("proposal go trace: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
