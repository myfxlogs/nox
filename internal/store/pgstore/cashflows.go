package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Cashflow position_cashflows 行。
type Cashflow struct {
	ID         int64
	PositionID int64
	Kind       string
	Venue      string
	AmountUSD  float64
	Ts         time.Time
}

// InsertCashflow 写入现金流。
func (s *Store) InsertCashflow(ctx context.Context, posID int64, kind, venue string, amount float64, ts time.Time) error {
	q := `INSERT INTO position_cashflows (position_id, kind, venue, amount_usd, ts)
		VALUES ($1, $2, $3, $4, $5)`
	_, err := s.pool.Exec(ctx, q, posID, kind, venue, amount, ts)
	return err
}

// ListCashflows 返回指定持仓的现金流（§13.7 复盘合算：realized_pnl + Σcashflows）。
func (s *Store) ListCashflows(ctx context.Context, posID int64) ([]Cashflow, error) {
	q := `SELECT id, position_id, kind, venue, amount_usd, ts
		FROM position_cashflows WHERE position_id = $1 ORDER BY ts`
	rows, err := s.pool.Query(ctx, q, posID)
	if err != nil {
		return nil, fmt.Errorf("list cashflows: %w", err)
	}
	defer rows.Close()
	var out []Cashflow
	for rows.Next() {
		var c Cashflow
		if err := rows.Scan(&c.ID, &c.PositionID, &c.Kind, &c.Venue, &c.AmountUSD, &c.Ts); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// HasCashflowForDay 检查指定持仓在某日是否已有某 kind 的现金流行（D-077 幂等：当日已记跳过）。
// day 为 UTC 日期（0 时），匹配该日 00:00~23:59:999 的行。
func (s *Store) HasCashflowForDay(ctx context.Context, posID int64, kind string, day time.Time) (bool, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	var one int
	err := s.pool.QueryRow(ctx,
		`SELECT 1 FROM position_cashflows
		 WHERE position_id = $1 AND kind = $2 AND ts >= $3 AND ts < $4 LIMIT 1`,
		posID, kind, start, end).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("check cashflow for day: %w", err)
}

// SumCashflowsNet 返回指定持仓 swap+funding+commission 现金流的 USD 合计（D-127 P4 返修①）。
// settlement.Settle 用此值算 net = realized + cashflowsNet，修正淘汰/复盘口径漏计息差/手续费的缺陷。
// commission 写入时已带负号（pipeline_legs.go:190 -fill.Commission），SUM 自动减 → 等价 swap+funding−commission。
// 无匹配行 → 返回 0（不影响 net）。
func (s *Store) SumCashflowsNet(ctx context.Context, posID int64) (float64, error) {
	var sum float64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_usd), 0) FROM position_cashflows
		 WHERE position_id = $1 AND kind IN ('swap','funding','commission')`,
		posID).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum cashflows net: %w", err)
	}
	return sum, nil
}
