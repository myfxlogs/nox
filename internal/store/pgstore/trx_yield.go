// D-258 S1：能量租金率序列查询（区间和+区间最早 ts）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// SumTRXTxRentalByRange D-258 S1：返回 [from, to) 区间 rental_income 之和（amount_sun）。
// 仿 SumCMCOvernightByRange 模式；from/to UTC 归一，from>=to 返回 0。
func (s *Store) SumTRXTxRentalByRange(ctx context.Context, from, to time.Time) (int64, error) {
	from = from.UTC()
	to = to.UTC()
	if !from.Before(to) {
		return 0, nil
	}
	var sum int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_sun), 0)
		FROM fin_trx_txs
		WHERE kind = 'rental_income' AND ts >= $1 AND ts < $2
	`, from, to).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum trx rental by range: %w", err)
	}
	return sum, nil
}

// FirstTRXTxRentalTsInRange D-258 S1：返回 [from, to) 区间最早的 rental_income ts。
// 空表/无匹配返回零值+false（供 TrxEnergyRentalAPR 判定实际样本天数）。
func (s *Store) FirstTRXTxRentalTsInRange(ctx context.Context, from, to time.Time) (time.Time, bool, error) {
	from = from.UTC()
	to = to.UTC()
	var ts time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(MIN(ts), '0001-01-01T00:00:00Z'::timestamptz)
		FROM fin_trx_txs
		WHERE kind = 'rental_income' AND ts >= $1 AND ts < $2
	`, from, to).Scan(&ts)
	if err != nil {
		return ts, false, fmt.Errorf("first trx rental ts in range: %w", err)
	}
	if ts.IsZero() {
		return ts, false, nil
	}
	return ts, true, nil
}

// AvgTRXVoteAprByRange D-258 S3：返回 [from, to) 区间 fin_trx_vote_apr.current_rate 均值。
// 空表/无匹配返回 false（冷启动不猜）。
func (s *Store) AvgTRXVoteAprByRange(ctx context.Context, from, to time.Time) (float64, bool, error) {
	from = from.UTC()
	to = to.UTC()
	var avg *float64
	err := s.pool.QueryRow(ctx, `
		SELECT AVG(current_rate) FROM fin_trx_vote_apr
		WHERE fetched_at >= $1 AND fetched_at < $2
	`, from, to).Scan(&avg)
	if err != nil {
		return 0, false, fmt.Errorf("avg trx vote apr by range: %w", err)
	}
	if avg == nil {
		return 0, false, nil
	}
	return *avg, true, nil
}

// EnergyDelegatedRangeInWindow 9-9 过渡期护栏：[from,to) 窗内 fin_trx_account 快照的
// energy_delegated min/max。max>min×1.2（min>0）=扩容/减仓过渡期——7d 窗读数分母失真，
// collector 跳过 floor 评估、雷达 RPC 置 transition 供前端灰显（D-258 尾巴，B' 教训）。
func (s *Store) EnergyDelegatedRangeInWindow(ctx context.Context, from, to time.Time) (int64, int64, error) {
	from = from.UTC()
	to = to.UTC()
	if !from.Before(to) {
		return 0, 0, nil
	}
	var minE, maxE int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(MIN(energy_delegated),0), COALESCE(MAX(energy_delegated),0)
		FROM fin_trx_account
		WHERE fetched_at >= $1 AND fetched_at < $2
	`, from, to).Scan(&minE, &maxE)
	if err != nil {
		return 0, 0, fmt.Errorf("energy delegated range in window: %w", err)
	}
	return minE, maxE, nil
}
