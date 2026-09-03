// position_daily_income（个人持仓实账化，财务线工作项 3）存储。
// 每日收益记录：一日一条（全组合计），energy/voting 属质押腿、interest 属空单腿，
// 结构化每日数值只进本表（设计文档 §3.2；职责边界 P3）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// DailyIncome 对应 position_daily_income 表（设计文档 §3.2）。
// Price/PnlUSD 为 nil = 记录时无报价（诚实空态，前端渲染 "—"）。
// Source = 'manual'（手工录入）或 'auto'（D-211 rollup 自动记账）。
type DailyIncome struct {
	ID          int64
	Day         time.Time
	EnergyTrx   float64
	VotingTrx   float64
	InterestUSD float64
	Price       *float64
	PnlUSD      *float64
	Note        string
	Source      string
	CreatedAt   time.Time
}

// AddDailyIncome upsert 一条每日收益记录：同 day 重复 → 覆盖（支持同日纠错），返回 id。
// price/pnlUSD 为 nil = 写 NULL（无报价时后端传 nil，不写 0 冒充现价）。
// D-211 F1：INSERT 与 ON CONFLICT DO UPDATE 两处均 stamp source='manual'（手工纠错盖章）。
func (s *Store) AddDailyIncome(ctx context.Context, day time.Time, energyTrx, votingTrx, interestUSD float64, price, pnlUSD *float64, note string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO position_daily_income (day, energy_trx, voting_trx, interest_usd, price, pnl_usd, note, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'manual')
		ON CONFLICT (day) DO UPDATE SET
			energy_trx   = EXCLUDED.energy_trx,
			voting_trx   = EXCLUDED.voting_trx,
			interest_usd = EXCLUDED.interest_usd,
			price        = EXCLUDED.price,
			pnl_usd      = EXCLUDED.pnl_usd,
			note         = EXCLUDED.note,
			source       = 'manual'
		RETURNING id
	`, day, energyTrx, votingTrx, interestUSD, price, pnlUSD, note).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add daily income: %w", err)
	}
	return id, nil
}

// AddDailyIncomeAuto D-211：auto 路径 upsert——条件更新 WHERE source='auto'，永不触碰 manual 行。
// 含业主对 auto 行的手工纠错——纠错后行已变 manual（AddDailyIncome stamp），后续 rollup 自动避让。
func (s *Store) AddDailyIncomeAuto(ctx context.Context, day time.Time, energyTrx, votingTrx, interestUSD float64, price, pnlUSD *float64, note string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO position_daily_income (day, energy_trx, voting_trx, interest_usd, price, pnl_usd, note, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'auto')
		ON CONFLICT (day) DO UPDATE SET
			energy_trx   = EXCLUDED.energy_trx,
			voting_trx   = EXCLUDED.voting_trx,
			interest_usd = EXCLUDED.interest_usd,
			price        = EXCLUDED.price,
			pnl_usd      = EXCLUDED.pnl_usd,
			note         = EXCLUDED.note,
			source       = 'auto'
		WHERE position_daily_income.source = 'auto'
	`, day, energyTrx, votingTrx, interestUSD, price, pnlUSD, note)
	if err != nil {
		return fmt.Errorf("add daily income auto: %w", err)
	}
	return nil
}

// ListDailyIncome 按 day 倒序返回每日收益；from/to 为可选区间（nil = 不设界）；limit≤0 默认 200。
// from/to 先转 UTC 日期字符串再比较：day 是 DATE 列，避免会话时区把边界日平移。
func (s *Store) ListDailyIncome(ctx context.Context, from, to *time.Time, limit, offset int) ([]DailyIncome, error) {
	if limit <= 0 {
		limit = 200
	}
	var fromStr, toStr *string
	if from != nil {
		v := from.UTC().Format("2006-01-02")
		fromStr = &v
	}
	if to != nil {
		v := to.UTC().Format("2006-01-02")
		toStr = &v
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, day, energy_trx, voting_trx, interest_usd, price, pnl_usd, note, source, created_at
		FROM position_daily_income
		WHERE ($1::date IS NULL OR day >= $1::date)
		  AND ($2::date IS NULL OR day <= $2::date)
		ORDER BY day DESC, id DESC
		LIMIT $3 OFFSET $4
	`, fromStr, toStr, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list daily income: %w", err)
	}
	defer rows.Close()
	var out []DailyIncome
	for rows.Next() {
		var d DailyIncome
		if err := rows.Scan(&d.ID, &d.Day, &d.EnergyTrx, &d.VotingTrx, &d.InterestUSD,
			&d.Price, &d.PnlUSD, &d.Note, &d.Source, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SumDailyIncomeInterest 累计利息 = SUM(interest_usd) over position_daily_income（全量）。
// 当前组合仅空单一处利息源，全量求和即可（设计文档 §4）。
func (s *Store) SumDailyIncomeInterest(ctx context.Context) (float64, error) {
	var sum float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(interest_usd), 0) FROM position_daily_income
	`).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum daily income interest: %w", err)
	}
	return sum, nil
}
