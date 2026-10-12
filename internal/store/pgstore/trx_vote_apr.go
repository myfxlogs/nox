// trx_vote_apr.go D-231 TRX 投票收益率（fin_trx_vote_apr）持久化——自 trx.go 拆出（D-309 纯移动，零行为变更）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// ---------- D-231 TRX 投票收益率（fin_trx_vote_apr） ----------
// TRXVoteApr 对应 fin_trx_vote_apr 表（5min 聚合一行）。
type TRXVoteApr struct {
	ID          int64
	FetchedAt   time.Time
	CurrentRate float64 // 票数加权平均年化 %
	CurrentSr   *string // 当前投票主 SR 名（票数最多者）；nil=无投票
	MaxRate     float64 // 市场最高年化 %（producer=true 降序第一）
	MaxSr       *string // 最高年化 SR 名；nil=无 producer
	MinRate     float64 // D-297：已投 SR 中最低年化 %
	MinSr       *string // D-297：最低年化 SR 名；nil=无投票
	Spread      float64 // max_rate − min_rate（pp，D-297 改口径）
}

// InsertTRXVoteApr 写一条投票收益率聚合行（5min 级，append-only）。
func (s *Store) InsertTRXVoteApr(ctx context.Context, a TRXVoteApr) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_trx_vote_apr
		  (fetched_at, current_rate, current_sr, max_rate, max_sr, spread, min_rate, min_sr)
		VALUES (COALESCE($1,now()),$2,$3,$4,$5,$6,$7,$8)
	`, a.FetchedAt, a.CurrentRate, a.CurrentSr, a.MaxRate, a.MaxSr, a.Spread, a.MinRate, a.MinSr)
	if err != nil {
		return fmt.Errorf("insert trx vote apr: %w", err)
	}
	return nil
}

// LastTRXVoteApr 返回最新一条投票收益率聚合行（按 fetched_at DESC）；空表返回 ErrNoRows。
func (s *Store) LastTRXVoteApr(ctx context.Context) (TRXVoteApr, error) {
	var a TRXVoteApr
	err := s.pool.QueryRow(ctx, `
		SELECT id, fetched_at, current_rate, current_sr, max_rate, max_sr, spread, min_rate, min_sr
		FROM fin_trx_vote_apr
		ORDER BY fetched_at DESC
		LIMIT 1
	`).Scan(&a.ID, &a.FetchedAt, &a.CurrentRate, &a.CurrentSr, &a.MaxRate, &a.MaxSr, &a.Spread, &a.MinRate, &a.MinSr)
	if err != nil {
		return a, fmt.Errorf("last trx vote apr: %w", err)
	}
	return a, nil
}
