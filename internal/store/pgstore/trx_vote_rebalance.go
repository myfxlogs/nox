// D-269 S1：TRX 投票收益优化器 Store 方法（fin_trx_vote_rebalance 表）。
package pgstore

import (
	"context"
	"fmt"
	"log"
	"time"
)

// TRXVoteRebalance 是 fin_trx_vote_rebalance 表的一行（D-269 §6.1）。
type TRXVoteRebalance struct {
	ID             int64
	DecidedAt      time.Time
	CurrentYield   float64
	TargetYield    float64
	ImprovementPct float64
	NeedRebalance  bool
	Reason         string
	TargetVotes    []byte // JSONB（[{"address":"T...","votes":5000},...]）
	ExecutedAt     *time.Time
	TxHash         *string
	Status         string // pending/executed/failed/skipped
	FailReason     *string
}

// InsertTrxVoteRebalance 插入一条决策记录，返回 id（D-269 §6.2）。
func (s *Store) InsertTrxVoteRebalance(ctx context.Context, rec TRXVoteRebalance) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO fin_trx_vote_rebalance
		 (current_yield, target_yield, improvement_pct, need_rebalance, reason, target_votes, status)
		 VALUES ($1, $2, $3, $4, $5, $6, COALESCE($7, 'pending'))
		 RETURNING id`,
		rec.CurrentYield, rec.TargetYield, rec.ImprovementPct, rec.NeedRebalance,
		rec.Reason, rec.TargetVotes, rec.Status).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert trx_vote_rebalance: %w", err)
	}
	return id, nil
}

// UpdateTrxVoteRebalanceResult 回填执行结果（txHash+status），executed_at=now()（D-269 §6.2）。
func (s *Store) UpdateTrxVoteRebalanceResult(ctx context.Context, id int64, txHash, status string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE fin_trx_vote_rebalance
		 SET executed_at = now(), tx_hash = $2, status = $3
		 WHERE id = $1`,
		id, txHash, status)
	if err != nil {
		return fmt.Errorf("update trx_vote_rebalance result: %w", err)
	}
	return nil
}

// TrxVoteRebalancedToday 查今日（业主时区 Asia/Shanghai）已执行改投次数是否达上限（D-269 §4.3 v1.2）。
// v1.2：MaxRebalancePerDay 参数 + 业主时区（非 UTC CURRENT_DATE）+ 查询失败 log.Printf。
func (s *Store) TrxVoteRebalancedToday(ctx context.Context, maxPerDay int) bool {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM fin_trx_vote_rebalance
		 WHERE status = 'executed'
		 AND (decided_at AT TIME ZONE 'Asia/Shanghai')::date = (now() AT TIME ZONE 'Asia/Shanghai')::date`).Scan(&count)
	if err != nil {
		log.Printf("[trxvote] TrxVoteRebalancedToday query failed: %v", err)
		return false // 查询失败保守返回 false（不阻塞决策），但有日志
	}
	return count >= maxPerDay
}

// GetLastTrxVoteRebalance 取最近一条决策记录（按 decided_at DESC LIMIT 1）。
func (s *Store) GetLastTrxVoteRebalance(ctx context.Context) (*TRXVoteRebalance, error) {
	var r TRXVoteRebalance
	var executedAt *time.Time
	var txHash, failReason *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, decided_at, current_yield, target_yield, improvement_pct,
		        need_rebalance, reason, target_votes, executed_at, tx_hash, status, fail_reason
		 FROM fin_trx_vote_rebalance ORDER BY decided_at DESC LIMIT 1`).Scan(
		&r.ID, &r.DecidedAt, &r.CurrentYield, &r.TargetYield, &r.ImprovementPct,
		&r.NeedRebalance, &r.Reason, &r.TargetVotes, &executedAt, &txHash, &r.Status, &failReason)
	if err != nil {
		return nil, err
	}
	r.ExecutedAt = executedAt
	r.TxHash = txHash
	r.FailReason = failReason
	return &r, nil
}
