package pgstore

import (
	"context"
	"fmt"
	"time"
)

// GetLatestFactTimestamp D-195-C1 S3：返回最新 fact 时间戳（facts_fresh readiness 层用）。
// 无 fact → 返回 zero time（调用方按 NOT_READY 处理）。
func (s *Store) GetLatestFactTimestamp(ctx context.Context) (time.Time, error) {
	var ts time.Time
	err := s.pool.QueryRow(ctx, `SELECT MAX(ts) FROM facts`).Scan(&ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("get latest fact ts: %w", err)
	}
	return ts, nil
}

// GetAccountIsReal D-195-C1 S2 第 3 条：按 accountKey（venue）查 is_real 标记。
// 现有 ListAccountIsReal 返回全部 venue→*bool 映射；本方法按单个 venue 查询。
// venue 不存在或 is_real=NULL → 返回 (false, nil)（fail-closed：未知视为非实盘）。
// 调用方需区分"未知"与"demo"——返回 *bool 版本见 GetAccountIsRealPtr。
func (s *Store) GetAccountIsReal(ctx context.Context, accountKey string) (bool, error) {
	var isReal *bool
	err := s.pool.QueryRow(ctx, `SELECT is_real FROM broker_accounts WHERE venue = $1`, accountKey).Scan(&isReal)
	if err != nil {
		return false, fmt.Errorf("get account is_real venue=%s: %w", accountKey, err)
	}
	if isReal == nil {
		return false, nil // 未知 → false（fail-closed）
	}
	return *isReal, nil
}
