// D-233 TRX 待领取实时化：fin_trx_cycle_vi 持久化（SSOT §S3）。
// 从 trx.go 提取以保 trx.go ≤300 行（check-lines 软限）。
package pgstore

import (
	"context"
	"fmt"
	"math/big"
	"time"
)

// TRXCycleVi 对应 fin_trx_cycle_vi 表（Vi 累积快照，upsert by cycle+sr_address）。
// Vi=BigInteger（10^18 scale，java-tron DECIMAL_OF_VI_REWARD=10^18）；
// user_votes=用户对该 SR 的票数（待领取=Σ vi×user_votes/10^18 用）。
type TRXCycleVi struct {
	ID        int64
	Cycle     int64
	SRAddress string
	Vi        *big.Int // BigInteger（NUMERIC 列，Go 侧 big.Int）
	UserVotes int64
	UpdatedAt time.Time
}

// UpsertTRXCycleVi upsert 一条 Vi 快照（by cycle+sr_address）。
// vi 以字符串形式传递（NUMERIC 列接受字符串输入，big.Int.Text() 输出十进制字符串）。
func (s *Store) UpsertTRXCycleVi(ctx context.Context, r TRXCycleVi) error {
	viStr := "0"
	if r.Vi != nil {
		viStr = r.Vi.Text(10)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_trx_cycle_vi (cycle, sr_address, vi, user_votes, updated_at)
		VALUES ($1,$2,$3,$4,COALESCE($5,now()))
		ON CONFLICT (cycle, sr_address) DO UPDATE SET vi = EXCLUDED.vi, user_votes = EXCLUDED.user_votes, updated_at = now()
	`, r.Cycle, r.SRAddress, viStr, r.UserVotes, r.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert trx cycle vi: %w", err)
	}
	return nil
}

// LatestTRXCycleVi 返回最新 cycle 的全部 SR Vi 行（待领取计算用）。
// 空=无 Vi 数据（GetHedgeStructure 回退到纯 allowance 显示）。
func (s *Store) LatestTRXCycleVi(ctx context.Context) ([]TRXCycleVi, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, cycle, sr_address, vi, user_votes, updated_at
		FROM fin_trx_cycle_vi
		WHERE cycle = (SELECT MAX(cycle) FROM fin_trx_cycle_vi)
		ORDER BY sr_address
	`)
	if err != nil {
		return nil, fmt.Errorf("latest trx cycle vi: %w", err)
	}
	defer rows.Close()
	var out []TRXCycleVi
	for rows.Next() {
		var r TRXCycleVi
		var viStr string
		if err := rows.Scan(&r.ID, &r.Cycle, &r.SRAddress, &viStr, &r.UserVotes, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Vi = new(big.Int)
		if _, ok := r.Vi.SetString(viStr, 10); !ok {
			return nil, fmt.Errorf("parse vi %q: invalid number", viStr)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MaxTRXCycleViCycle 返回当前最大 cycle 号（VotesCollector 重启恢复用）；空表返回 0。
func (s *Store) MaxTRXCycleViCycle(ctx context.Context) (int64, error) {
	var cycle int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(cycle), 0) FROM fin_trx_cycle_vi`).Scan(&cycle)
	if err != nil {
		return 0, fmt.Errorf("max trx cycle vi cycle: %w", err)
	}
	return cycle, nil
}
