// income_events 存储（D-334ab 位点①）：binance FUNDING_FEE 实数轨。
// estimated 轨（position_cashflows，swapledger 每腿每日单行）不动；本表=venue 侧实结算流水，
// tranId 三列唯一幂等 + OPEN 腿匹配位可空（唯一命中才填，零/多义留 NULL 不猜）。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// IncomeEvent income_events 行（PositionID/LegNo=匹配命中才非 nil）。
type IncomeEvent struct {
	Venue      string
	AccountID  int64
	Symbol     string
	IncomeType string
	AmountUSD  float64
	TranID     int64
	Ts         time.Time
	PositionID *int64
	LegNo      *int32
}

// MaxIncomeEventTS 采集游标：该 venue 账户已入账最大 ts（表内派生零新状态，重启安全）。
// 空表 → (零值,false,nil)。
func (s *Store) MaxIncomeEventTS(ctx context.Context, venue string, accountID int64) (time.Time, bool, error) {
	var ts *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT MAX(ts) FROM income_events WHERE venue = $1 AND account_id = $2`, venue, accountID).Scan(&ts)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("max income ts: %w", err)
	}
	if ts == nil {
		return time.Time{}, false, nil
	}
	return *ts, true, nil
}

// InsertIncomeEvents 批量幂等写入（唯一键 (venue,account_id,tran_id) 冲突 DO NOTHING）。
// 返回实插行数（幂等重跑=0）。
func (s *Store) InsertIncomeEvents(ctx context.Context, evs []IncomeEvent) (int64, error) {
	if len(evs) == 0 {
		return 0, nil
	}
	batch := &pgx.Batch{}
	for _, e := range evs {
		batch.Queue(`
			INSERT INTO income_events (venue, account_id, symbol, income_type, amount_usd, tran_id, ts, position_id, leg_no)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (venue, account_id, tran_id) DO NOTHING`,
			e.Venue, e.AccountID, e.Symbol, e.IncomeType, e.AmountUSD, e.TranID, e.Ts, e.PositionID, e.LegNo)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	var written int64
	for range evs {
		cmd, err := br.Exec()
		if err != nil {
			return written, fmt.Errorf("insert income event: %w", err)
		}
		written += cmd.RowsAffected()
	}
	return written, nil
}

// SumIncomeForDay D-349 S1：CEX perp 腿对账实际值——venue+symbol 当日 income 实收日和。
// 账户级口径（不绑 position_id——income 行 position_id 匹配为账本增强非对账前提，D-334ab 可空）。
// 返回 (金额和, 行数)；行数供「0 行=未到结算窗」语义（无行≠实收 0）。
func (s *Store) SumIncomeForDay(ctx context.Context, venue, symbol string, day time.Time) (float64, int, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	var sum float64
	var rows int
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_usd), 0), COUNT(*) FROM income_events
		 WHERE venue = $1 AND symbol = $2 AND ts >= $3 AND ts < $4`,
		venue, symbol, start, end).Scan(&sum, &rows)
	if err != nil {
		return 0, 0, fmt.Errorf("sum income for day: %w", err)
	}
	return sum, rows, nil
}

// MatchOpenLegForIncome income 行→OPEN 仓腿匹配：venue+symbol 在活跃仓（四态，同
// positions_active_dedup_key_idx 口径）OPEN 腿中**唯一命中**才返回 (posID, legNo, 1)；
// 零命中 → (0,0,0)；多义 → (last, hits>1)——调用方按 hits==1 才采信（不猜，venue 侧可重查）。
func (s *Store) MatchOpenLegForIncome(ctx context.Context, venue, symbol string) (posID int64, legNo int16, hits int, err error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, l.leg_no
		FROM position_legs l
		JOIN positions p ON p.id = l.position_id
		WHERE l.venue = $1 AND l.symbol = $2 AND l.phase = 'OPEN'
		  AND p.status IN ('OPENING','OPEN','LEGGED','CLOSING')`, venue, symbol)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("match open leg: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&posID, &legNo); err != nil {
			return 0, 0, 0, err
		}
		hits++
	}
	return posID, legNo, hits, rows.Err()
}

// OpenLegSymbols D-334ab S3：指定 venue 活跃仓（四态）OPEN 腿的 distinct symbol 集
// （income 结算对齐定时器的符号集=白名单∪本查询）。
func (s *Store) OpenLegSymbols(ctx context.Context, venue string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT l.symbol
		FROM position_legs l
		JOIN positions p ON p.id = l.position_id
		WHERE l.venue = $1 AND l.phase = 'OPEN'
		  AND p.status IN ('OPENING','OPEN','LEGGED','CLOSING')`, venue)
	if err != nil {
		return nil, fmt.Errorf("open leg symbols: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sym string
		if err := rows.Scan(&sym); err != nil {
			return nil, err
		}
		out = append(out, sym)
	}
	return out, rows.Err()
}
