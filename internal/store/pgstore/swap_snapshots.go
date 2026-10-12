package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arb/internal/data/fact"
	"arb/internal/data/keydomain"

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
// P0-1：venue 必须是完整 venueKey（如 "vA:mt5"），与 position_legs.venue 同口径；
// facts.venue 是 bare venue，两者不可混用。
func (s *Store) InsertSwapSnapshot(ctx context.Context, posID int64, venue string, swapUSD float64, ts time.Time) error {
	vk := keydomain.Parse(venue) // D-496：venueKey 拆裸键+通道型
	_, err := s.pool.Exec(ctx,
		`INSERT INTO position_swap_snapshots (position_id, venue, venue_type, swap_usd, ts) VALUES ($1, $2, $3, $4, $5)`,
		posID, vk.Venue, vk.Type, swapUSD, ts)
	return err
}

// LatestSwapSnapshot 取指定持仓+venue 的最新一条快照。无行返回 pgx.ErrNoRows。
// P0-1：venue 是完整 venueKey（如 "vA:mt5"），禁止传 bare venue。
func (s *Store) LatestSwapSnapshot(ctx context.Context, posID int64, venue string) (SwapSnapshot, error) {
	var snap SwapSnapshot
	err := s.pool.QueryRow(ctx,
		`SELECT id, position_id, venue_key AS venue, swap_usd, ts
		 FROM position_swap_snapshots
		 WHERE position_id = $1 AND venue_key = $2
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
// P0-1：venue 是完整 venueKey（如 "vA:mt5"），禁止传 bare venue。
func (s *Store) LatestSwapSnapshotBefore(ctx context.Context, posID int64, venue string, before time.Time) (SwapSnapshot, error) {
	var snap SwapSnapshot
	err := s.pool.QueryRow(ctx,
		`SELECT id, position_id, venue_key AS venue, swap_usd, ts
		 FROM position_swap_snapshots
		 WHERE position_id = $1 AND venue_key = $2 AND ts < $3
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
// P0-1：venue 是完整 venueKey（如 "vA:mt5"），禁止传 bare venue。
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

// HasNonZeroSwapHistory D-165 S3：查该 venue 是否有 swap_usd != 0 的历史记录（不限 posID）。
// 用于 swap facts 零值校验：facts=0 但 broker 实际收 swap → 规格声明与实际不一致 → fail-closed。
// P0-1：venue 是完整 venueKey（如 "vA:mt5"），禁止传 bare venue。
func (s *Store) HasNonZeroSwapHistory(ctx context.Context, venue string) (bool, error) {
	var has bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM position_swap_snapshots WHERE venue_key = $1 AND swap_usd != 0)`,
		venue).Scan(&has)
	if err != nil {
		return false, fmt.Errorf("has non-zero swap history: %w", err)
	}
	return has, nil
}

// ListSwapSnapshotsByPosID D-182 S1：列指定持仓的全部 swap 快照（按 venue, ts 排序）。
// scanAndClose 周期零检测用——按 posID 拉序列，避免全量扫描。
func (s *Store) ListSwapSnapshotsByPosID(ctx context.Context, posID int64) ([]SwapSnapshot, error) {
	q := `SELECT id, position_id, venue_key AS venue, swap_usd, ts
	      FROM position_swap_snapshots
	      WHERE position_id = $1
	      ORDER BY venue, ts`
	rows, err := s.pool.Query(ctx, q, posID)
	if err != nil {
		return nil, fmt.Errorf("list swap snapshots by pos id: %w", err)
	}
	defer rows.Close()
	var out []SwapSnapshot
	for rows.Next() {
		var snap SwapSnapshot
		if err := rows.Scan(&snap.ID, &snap.PositionID, &snap.Venue, &snap.SwapUSD, &snap.Ts); err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

// ListAllSwapSnapshots D-180 S1：列全部 swap 快照（按 position_id, venue, ts 排序），boot 存量回溯用。
func (s *Store) ListAllSwapSnapshots(ctx context.Context) ([]SwapSnapshot, error) {
	q := `SELECT id, position_id, venue_key AS venue, swap_usd, ts
	      FROM position_swap_snapshots
	      ORDER BY position_id, venue_key, ts`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list all swap snapshots: %w", err)
	}
	defer rows.Close()
	var out []SwapSnapshot
	for rows.Next() {
		var snap SwapSnapshot
		if err := rows.Scan(&snap.ID, &snap.PositionID, &snap.Venue, &snap.SwapUSD, &snap.Ts); err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

// FactExistsAt D-180 S1：检查指定 kind+venue+symbol+ts 的 fact 是否已存在（幂等键检查）。
func (s *Store) FactExistsAt(ctx context.Context, kind, venue, symbol string, ts time.Time) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		// symbol NULL=非品种域；传 '' 匹配 NULL 行（NOT DISTINCT FROM）。
		`SELECT EXISTS(SELECT 1 FROM facts WHERE kind = $1 AND (venue_key = $2 OR venue = $2) AND symbol IS NOT DISTINCT FROM NULLIF($3,'') AND ts = $4)`,
		kind, venue, symbol, ts).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("fact exists at: %w", err)
	}
	return exists, nil
}

// FactsWithin D-180 S2：查指定 kind+venue+symbol 在 since 之后的全部 facts（swapRateFor 7d 窗口用）。
func (s *Store) FactsWithin(ctx context.Context, kind, venue, symbol string, since time.Time) ([]fact.Fact, error) {
	q := `SELECT kind, venue, COALESCE(symbol,'') AS symbol, value, unit, ts, src
	      FROM facts
	      WHERE ($1 = '' OR kind = $1)
	        AND ($2 = '' OR venue_key = $2 OR venue = $2)
	        AND ($3 = '' OR symbol = $3 OR symbol IS NULL)
	        AND ts >= $4
	      ORDER BY ts DESC`
	rows, err := s.pool.Query(ctx, q, kind, venue, symbol, since)
	if err != nil {
		return nil, fmt.Errorf("facts within: %w", err)
	}
	defer rows.Close()
	var out []fact.Fact
	for rows.Next() {
		var f fact.Fact
		if err := rows.Scan(&f.Kind, &f.Venue, &f.Symbol, &f.Value, &f.Unit, &f.Ts, &f.Src); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FundingWindowAvg D-035：funding facts 窗内聚合单条读（AVG/MAX/COUNT——Go 侧零行搬运）；
// fact.FundingMeanAnnual 的数据源。n=0（窗内零样本）→ avg/maxTs 零值，调用方按 n 判 fail-closed。
func (s *Store) FundingWindowAvg(ctx context.Context, venue, symbol string, since time.Time) (float64, time.Time, int, error) {
	var avg float64
	var maxTs *time.Time
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(AVG(value),0), MAX(ts), COUNT(*)
		 FROM facts WHERE kind='funding' AND venue=$1 AND symbol=$2 AND ts>$3`,
		venue, symbol, since).Scan(&avg, &maxTs, &n)
	if err != nil {
		return 0, time.Time{}, 0, fmt.Errorf("funding window avg: %w", err)
	}
	var ts time.Time
	if maxTs != nil {
		ts = *maxTs
	}
	return avg, ts, n, nil
}

// SumCashflowsForDay SUM(amount_usd) WHERE kind+posID+venue+day 范围（D-110 P2 对账用）。
// day 范围同 HasCashflowForLegDay（D-159 W3a R5 更名）：start=time.Date(year,month,day,0,0,0,0,UTC) / end=start.AddDate(0,0,1)。
func (s *Store) SumCashflowsForDay(ctx context.Context, posID int64, kind, venue string, day time.Time) (float64, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	var sum float64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_usd), 0) FROM position_cashflows
		 WHERE position_id = $1 AND kind = $2 AND (venue_key = $3 OR venue = $3) AND ts >= $4 AND ts < $5`,
		posID, kind, venue, start, end).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum cashflows for day: %w", err)
	}
	return sum, nil
}
