// fin_trx_vote_apr_top 表存储层（全 SR top5 年化持久化，身份争议逐 SR 复核）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// TRXVoteAprTop 对应 fin_trx_vote_apr_top 表（D-249 S3：全 SR top5 年化持久化）。
type TRXVoteAprTop struct {
	ID             int64
	FetchedAt      time.Time
	SrName         string
	AnnualizedRate float64
}

// InsertTRXVoteAprTopBatch 批量写入 top-N producer 年化行（每 Poll 落 top-5，append-only）。
// fetchedAt 为本轮采集时间（所有行同一时间戳）；rows 须已按 rate 降序排列。
func (s *Store) InsertTRXVoteAprTopBatch(ctx context.Context, fetchedAt time.Time, rows []TRXVoteAprTop) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("insert trx vote apr top batch (begin): %w", err)
	}
	defer tx.Rollback(ctx)
	for _, r := range rows {
		if _, err := tx.Exec(ctx, `
			INSERT INTO fin_trx_vote_apr_top (fetched_at, sr_name, annualized_rate)
			VALUES ($1, $2, $3)
		`, fetchedAt, r.SrName, r.AnnualizedRate); err != nil {
			return fmt.Errorf("insert trx vote apr top batch (row): %w", err)
		}
	}
	return tx.Commit(ctx)
}

// LatestTRXVoteAprTop 返回最新一批 top-N producer 年化行（按 fetched_at DESC 取最新批次，再按 annualized_rate DESC）。
func (s *Store) LatestTRXVoteAprTop(ctx context.Context) ([]TRXVoteAprTop, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, fetched_at, sr_name, annualized_rate
		FROM fin_trx_vote_apr_top
		WHERE fetched_at = (SELECT MAX(fetched_at) FROM fin_trx_vote_apr_top)
		ORDER BY annualized_rate DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("latest trx vote apr top: %w", err)
	}
	defer rows.Close()
	var out []TRXVoteAprTop
	for rows.Next() {
		var r TRXVoteAprTop
		if err := rows.Scan(&r.ID, &r.FetchedAt, &r.SrName, &r.AnnualizedRate); err != nil {
			return nil, fmt.Errorf("scan trx vote apr top: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
