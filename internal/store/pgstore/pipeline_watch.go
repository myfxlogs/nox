// pipeline_watch.go —— 发现漏斗堵点探针（PraisonAI 对照评估 §6.1③ 系统自盯版）。
// 三段等推进态积压+最老入队时戳：假设等裁（fin_opp_hypothesis.proposed，memo
// 裁决段）、提案等裁（fin_proposal.proposed）、毕业评审（carry_graduations.pending
// ——unqualified=graduate_stale 已判摘牌带复活通道，非等推进态不入计）。
// 消费方=cmd/core pipeline_watch collector（积压>阈 且 停滞>阈 → warn 告警）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// FunnelStageLoad 单段积压载荷（Oldest=nil=零行，停滞维无意义）。
type FunnelStageLoad struct {
	Stage  string
	Count  int64
	Oldest *time.Time
}

// PipelineFunnelBacklog 三段等推进行数+最老入队时戳。只读，零行段照返（Count=0）。
func (s *Store) PipelineFunnelBacklog(ctx context.Context) ([]FunnelStageLoad, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT 'hypothesis_ruling', count(*), min(first_seen)
		  FROM fin_opp_hypothesis WHERE status = 'proposed'
		UNION ALL
		SELECT 'proposal_ruling', count(*), min(created_at)
		  FROM fin_proposal WHERE status = 'proposed'
		UNION ALL
		SELECT 'graduation_eval', count(*), min(created_at)
		  FROM carry_graduations WHERE status = 'pending'`)
	if err != nil {
		return nil, fmt.Errorf("pipeline funnel backlog: %w", err)
	}
	defer rows.Close()
	var out []FunnelStageLoad
	for rows.Next() {
		var l FunnelStageLoad
		if err := rows.Scan(&l.Stage, &l.Count, &l.Oldest); err != nil {
			return nil, fmt.Errorf("pipeline funnel backlog scan: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
