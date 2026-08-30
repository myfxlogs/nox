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
// D-159 W3a R5：加 legNo 参数（per-leg 幂等键前置）。
// legNo 是 position_legs.leg_no（int16，从 0 开始，0 是有效第一腿，写 0 不是 NULL）。
func (s *Store) InsertCashflow(ctx context.Context, posID int64, legNo int16, kind, venue string, amount float64, ts time.Time) error {
	q := `INSERT INTO position_cashflows (position_id, leg_no, kind, venue, amount_usd, ts)
		VALUES ($1, $2, $3, $4, $5, $6)`
	_, err := s.pool.Exec(ctx, q, posID, legNo, kind, venue, amount, ts)
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

// HasCashflowForLegDay 检查指定持仓某腿在某日是否已有某 kind 的现金流行（D-159 W3a R5 per-leg 幂等）。
// day 为 UTC 日期（0 时），匹配该日 00:00~23:59:999 的行；替代 position 级 HasCashflowForDay
// （position 级短路会让失败腿永久漏记，R5：一腿失败下一轮只补失败腿）。
func (s *Store) HasCashflowForLegDay(ctx context.Context, posID int64, legNo int16, kind string, day time.Time) (bool, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	var one int
	err := s.pool.QueryRow(ctx,
		`SELECT 1 FROM position_cashflows
		 WHERE position_id = $1 AND leg_no = $2 AND kind = $3 AND ts >= $4 AND ts < $5 LIMIT 1`,
		posID, legNo, kind, start, end).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("check cashflow for leg day: %w", err)
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

// SumFundingCashflows 返回指定持仓 funding 现金流的 USD 合计（D-146 S5 回本口径扩展）。
// enrich 回本计算用：CEX 仓（PERP_FUNDING/FUNDING_HEDGE）的 funding 实收并入日均收益。
// 无匹配行 → 返回 0（纯 MT 仓无 funding 流，行为不变）。
func (s *Store) SumFundingCashflows(ctx context.Context, posID int64) (float64, error) {
	var sum float64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_usd), 0) FROM position_cashflows
		 WHERE position_id = $1 AND kind = 'funding'`,
		posID).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum funding cashflows: %w", err)
	}
	return sum, nil
}

// SumCashflowsByPosVenue 批量返回 (position_id, venue) → swap+funding 合计（D-152 微件①）。
// broker_position_build 用：CEX 腿按 (position_id, venue) 聚合 cashflows 填 swap 字段。
// key 格式 = posID\x00venue；无匹配行 → 该 key 不在 map 中（调用方按 0 处理）。
func (s *Store) SumCashflowsByPosVenue(ctx context.Context, posIDs []int64) (map[string]float64, error) {
	out := make(map[string]float64)
	if len(posIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT position_id, venue, COALESCE(SUM(amount_usd), 0)
		 FROM position_cashflows
		 WHERE position_id = ANY($1) AND kind IN ('swap','funding')
		 GROUP BY position_id, venue`,
		posIDs)
	if err != nil {
		return nil, fmt.Errorf("sum cashflows by pos venue: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var posID int64
		var venue string
		var sum float64
		if err := rows.Scan(&posID, &venue, &sum); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%d\x00%s", posID, venue)] = sum
	}
	return out, rows.Err()
}
