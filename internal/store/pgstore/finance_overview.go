// finance_overview.go D-348：财务总览聚合查询（三个数+收益分解+统一流水；后端算，前端零信任零计算）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// FinancePnlEnv 已实现 PnL 按环境分层行（real/demo）。
type FinancePnlEnv struct {
	Environment string
	RealizedPnl float64
	Closed      int
}

// FinanceIncomeKind 收入构成按类型行（funding/swap/commission）。
type FinanceIncomeKind struct {
	Kind     string
	TotalUSD float64
	Rows     int
}

// FinanceFlow 统一流水行（三源合并：income_events 结算 / cashflows 实数轨 / ledger 台账）。
type FinanceFlow struct {
	Ts         time.Time
	Source     string
	Kind       string
	Symbol     string
	AmountUSD  float64
	PositionID int64
	Note       string
}

// LedgerDepositedTotal 台账合计（正=入金）。amount 为台账原币列（ledger 无 amount_usd 列），
// 原币直加=既有 LedgerSummary 同款口径（D-348 施工注：任务书列名与实库不符，按台账语义取 amount）。
func (s *Store) LedgerDepositedTotal(ctx context.Context) (float64, error) {
	var v float64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount),0) FROM ledger`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("ledger deposited total: %w", err)
	}
	return v, nil
}

// FinancePnlByEnv 已实现 PnL 按环境分层（status=CLOSED；environment NULL=legacy real 链路——
// D-195-B 前旧链路无环境列，D-348 锚：NULL 桶合计=设计 real 桶 −3169.94）。
func (s *Store) FinancePnlByEnv(ctx context.Context) ([]FinancePnlEnv, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(environment, 'real') AS env,
		       COALESCE(SUM(realized_pnl), 0),
		       COUNT(*)
		FROM positions
		WHERE status = 'CLOSED'
		GROUP BY 1 ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("finance pnl by env: %w", err)
	}
	defer rows.Close()
	var out []FinancePnlEnv
	for rows.Next() {
		var r FinancePnlEnv
		if err := rows.Scan(&r.Environment, &r.RealizedPnl, &r.Closed); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FinanceIncomeByKind 收入构成（cashflows 全量口径含 estimated——D-348 锚
// funding 49/swap 50/commission 7 为全量分组；estimated 与 flows 的双计边界仅作用于 flows）。
func (s *Store) FinanceIncomeByKind(ctx context.Context) ([]FinanceIncomeKind, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT kind, COALESCE(SUM(amount_usd), 0), COUNT(*)
		FROM position_cashflows
		GROUP BY kind ORDER BY kind`)
	if err != nil {
		return nil, fmt.Errorf("finance income by kind: %w", err)
	}
	defer rows.Close()
	var out []FinanceIncomeKind
	for rows.Next() {
		var r FinanceIncomeKind
		if err := rows.Scan(&r.Kind, &r.TotalUSD, &r.Rows); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FinanceFlows 统一流水三源合并，ts 倒序 LIMIT limit。
// 防漏计：cashflows quality IS NULL 轨必入（okx/MT 结算实数，income_events 不覆盖）。
// 防双计：quality='estimated' 预测轨排除。挂账（任务书 T2 锚）：binance funding NULL 轨止于
// 8-30（income collector 上线前）与 income_events 自 9-18 时间轴天然不重叠；若未来重叠须升级 dedup 键。
func (s *Store) FinanceFlows(ctx context.Context, limit int) ([]FinanceFlow, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ts, source, kind, symbol, amount_usd, position_id, note FROM (
			SELECT ie.ts, ie.venue AS source, LOWER(ie.income_type) AS kind, ie.symbol,
			       ie.amount_usd, COALESCE(ie.position_id, 0)::BIGINT AS position_id, '' AS note
			FROM income_events ie
			UNION ALL
			SELECT cf.ts, cf.venue, cf.kind, '' AS symbol, cf.amount_usd,
			       cf.position_id::BIGINT, '' AS note
			FROM position_cashflows cf
			WHERE COALESCE(cf.quality, '') <> 'estimated'
			UNION ALL
			SELECT l.date, 'ledger', l.channel, '' AS symbol, l.amount, 0::BIGINT, l.note
			FROM ledger l
		) f
		ORDER BY ts DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("finance flows: %w", err)
	}
	defer rows.Close()
	var out []FinanceFlow
	for rows.Next() {
		var r FinanceFlow
		if err := rows.Scan(&r.Ts, &r.Source, &r.Kind, &r.Symbol, &r.AmountUSD, &r.PositionID, &r.Note); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
