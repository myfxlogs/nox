package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SwapSnapshot position_swap_snapshots 行（D-110 P2）。
// SwapUSD 是 broker 持仓 swap 累计值（从开仓到当前），非日增量。
type SwapSnapshot struct {
	ID         int64
	PositionID int64
	Venue      string
	SwapUSD    float64
	Ts         time.Time
}

// InsertSwapSnapshot 写入 swap 累计值快照。
func (s *Store) InsertSwapSnapshot(ctx context.Context, posID int64, venue string, swapUSD float64, ts time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO position_swap_snapshots (position_id, venue, swap_usd, ts) VALUES ($1, $2, $3, $4)`,
		posID, venue, swapUSD, ts)
	return err
}

// LatestSwapSnapshot 取指定持仓+venue 的最新一条快照。无行返回 pgx.ErrNoRows。
func (s *Store) LatestSwapSnapshot(ctx context.Context, posID int64, venue string) (SwapSnapshot, error) {
	var snap SwapSnapshot
	err := s.pool.QueryRow(ctx,
		`SELECT id, position_id, venue, swap_usd, ts
		 FROM position_swap_snapshots
		 WHERE position_id = $1 AND venue = $2
		 ORDER BY ts DESC LIMIT 1`,
		posID, venue).Scan(&snap.ID, &snap.PositionID, &snap.Venue, &snap.SwapUSD, &snap.Ts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SwapSnapshot{}, err
		}
		return SwapSnapshot{}, fmt.Errorf("latest swap snapshot: %w", err)
	}
	return snap, nil
}

// LatestSwapSnapshotBefore 取 before 之前最新一条快照（对账用：昨日末条）。
func (s *Store) LatestSwapSnapshotBefore(ctx context.Context, posID int64, venue string, before time.Time) (SwapSnapshot, error) {
	var snap SwapSnapshot
	err := s.pool.QueryRow(ctx,
		`SELECT id, position_id, venue, swap_usd, ts
		 FROM position_swap_snapshots
		 WHERE position_id = $1 AND venue = $2 AND ts < $3
		 ORDER BY ts DESC LIMIT 1`,
		posID, venue, before).Scan(&snap.ID, &snap.PositionID, &snap.Venue, &snap.SwapUSD, &snap.Ts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SwapSnapshot{}, err
		}
		return SwapSnapshot{}, fmt.Errorf("latest swap snapshot before: %w", err)
	}
	return snap, nil
}

// DailySwapAccrued 当日 swap 增量 = 今日末条 swap_usd - 昨日末条 swap_usd。
// day 为 UTC 日期。无快照或今日无新快照（今日末条 ts <= day_start）返回 ok=false。
func (s *Store) DailySwapAccrued(ctx context.Context, posID int64, venue string, day time.Time) (float64, bool, error) {
	dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	today, err := s.LatestSwapSnapshot(ctx, posID, venue)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if !today.Ts.After(dayStart) {
		return 0, false, nil // 今日末条 ts <= day_start → 今日未结算
	}
	yesterday, err := s.LatestSwapSnapshotBefore(ctx, posID, venue, dayStart)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil // 无昨日快照
		}
		return 0, false, err
	}
	return today.SwapUSD - yesterday.SwapUSD, true, nil
}

// SumCashflowsForDay SUM(amount_usd) WHERE kind+posID+venue+day 范围（D-110 P2 对账用）。
// day 范围同 HasCashflowForDay：start=time.Date(year,month,day,0,0,0,0,UTC) / end=start.AddDate(0,0,1)。
func (s *Store) SumCashflowsForDay(ctx context.Context, posID int64, kind, venue string, day time.Time) (float64, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	var sum float64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_usd), 0) FROM position_cashflows
		 WHERE position_id = $1 AND kind = $2 AND venue = $3 AND ts >= $4 AND ts < $5`,
		posID, kind, venue, start, end).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum cashflows for day: %w", err)
	}
	return sum, nil
}
