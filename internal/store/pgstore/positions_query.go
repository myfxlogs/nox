package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ListPositions 分页返回持仓。
// D-106：status="ACTIVE" 特殊值 → WHERE status IN (OPEN/OPENING/LEGGED/CLOSING)（在仓 Tab）；
// 排序改 ORDER BY COALESCE(closed_at, opened_at) DESC NULLS LAST（历史视图默认序，OPEN 行排前）。
func (s *Store) ListPositions(ctx context.Context, status string, limit, offset int) ([]Position, error) {
	if limit <= 0 {
		limit = 50
	}
	if status == "ACTIVE" {
		// ACTIVE 路径不绑定 $1（IN 子句无参数），limit/offset 用 $1/$2。
		q := `SELECT id, opportunity_id, type, status, COALESCE(expected_net_bps, 0), expected_entry_spread_bps, expected_swap_bps_daily, expected_annual_pct, realized_pnl, opened_at, closed_at, note
			FROM positions
			WHERE status IN ('OPEN','OPENING','LEGGED','CLOSING')
			ORDER BY COALESCE(closed_at, opened_at) DESC NULLS LAST LIMIT $1 OFFSET $2`
		rows, err := s.pool.Query(ctx, q, limit, offset)
		if err != nil {
			return nil, fmt.Errorf("list positions: %w", err)
		}
		defer rows.Close()
		return scanPositions(rows)
	}
	q := `SELECT id, opportunity_id, type, status, COALESCE(expected_net_bps, 0), expected_entry_spread_bps, expected_swap_bps_daily, expected_annual_pct, realized_pnl, opened_at, closed_at, note
		FROM positions
		WHERE ($1 = '' OR status = $1)
		ORDER BY COALESCE(closed_at, opened_at) DESC NULLS LAST LIMIT $2 OFFSET $3`
	rows, err := s.pool.Query(ctx, q, status, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list positions: %w", err)
	}
	defer rows.Close()
	return scanPositions(rows)
}

// scanPositions 公共行扫描（ListPositions 两条路径共用）。
func scanPositions(rows pgx.Rows) ([]Position, error) {
	var out []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.ID, &p.OpportunityID, &p.Type, &p.Status, &p.ExpectedNetBps,
			&p.ExpectedEntrySpreadBps, &p.ExpectedSwapBpsDaily, &p.ExpectedAnnualPct,
			&p.RealizedPnl, &p.OpenedAt, &p.ClosedAt, &p.Note); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountPositions 返回符合筛选条件的持仓总数（D-069 分页控件用）。
// D-106：status="ACTIVE" 同 ListPositions 映射 4 活跃态。
func (s *Store) CountPositions(ctx context.Context, status string) (int, error) {
	var n int
	var err error
	if status == "ACTIVE" {
		err = s.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM positions WHERE status IN ('OPEN','OPENING','LEGGED','CLOSING')`).Scan(&n)
	} else {
		err = s.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM positions WHERE ($1 = '' OR status = $1)`, status).Scan(&n)
	}
	if err != nil {
		return 0, fmt.Errorf("count positions: %w", err)
	}
	return n, nil
}

// ListActivePositions 返回所有活跃态持仓（OPENING/OPEN/LEGGED/CLOSING）。
// D-080 R1：gate 防重改 DB 实时派生——替代内存 openCombos 集，治手动路径旁路。
// D-159 W1a R2：UNKNOWN position 用 CLOSING+uncertainty_reason 识别（不新增 DB 枚举），CLOSING 已在列。
// 行数=MaxPositions 量级，成本≈COUNT；返回行供 gate 派生 combo.FromOppID 匹配。
func (s *Store) ListActivePositions(ctx context.Context) ([]Position, error) {
	q := `SELECT id, opportunity_id, type, status, COALESCE(expected_net_bps, 0), expected_entry_spread_bps, expected_swap_bps_daily, expected_annual_pct, realized_pnl, opened_at, closed_at, note, notional_usd
		FROM positions
		WHERE status IN ('OPENING','OPEN','LEGGED','CLOSING')
		ORDER BY id`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list active positions: %w", err)
	}
	defer rows.Close()
	var out []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.ID, &p.OpportunityID, &p.Type, &p.Status, &p.ExpectedNetBps,
			&p.ExpectedEntrySpreadBps, &p.ExpectedSwapBpsDaily, &p.ExpectedAnnualPct,
			&p.RealizedPnl, &p.OpenedAt, &p.ClosedAt, &p.Note, &p.NotionalUSD); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountOpenPositions 返回 OPEN+OPENING+LEGGED 持仓数（风控用）。
func (s *Store) CountOpenPositions(ctx context.Context) (int, error) {
	q := `SELECT count(*) FROM positions WHERE status IN ('OPEN','OPENING','LEGGED')`
	var n int
	err := s.pool.QueryRow(ctx, q).Scan(&n)
	return n, err
}

// HasOpenPositionByOpportunity 返回该机会是否已有未平仓持仓（D-058 幂等防重复执行）。
// CLOSED/FAILED 不在列 → 已闭合/已失败的机会可重入（再执行是新的执行意图，非重复提交）。
// D-159 W1a R2：UNKNOWN position（CLOSING+uncertainty_reason）计入防重——人工确认前禁止重入防双开仓；
// 正常 CLOSING（uncertainty_reason IS NULL）不计入（平仓中可重开是既有行为，不破坏现有流程）。
func (s *Store) HasOpenPositionByOpportunity(ctx context.Context, opportunityID string) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx,
		`SELECT 1 FROM positions WHERE opportunity_id = $1 AND (
			status IN ('OPEN','OPENING','LEGGED')
			OR (status = 'CLOSING' AND uncertainty_reason IS NOT NULL)
		) LIMIT 1`,
		opportunityID).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("check open position by opportunity: %w", err)
}

// ListOpenPositionsByType 返回指定类型且状态为 OPEN 的持仓（D-077 swap 逐日记账用）。
// 仅 OPEN（不含 OPENING/LEGGED——未完全成交的腿无 swap 记账语义）。
func (s *Store) ListOpenPositionsByType(ctx context.Context, types []string) ([]Position, error) {
	if len(types) == 0 {
		return nil, nil
	}
	q := `SELECT id, opportunity_id, type, status, COALESCE(expected_net_bps, 0), expected_entry_spread_bps, expected_swap_bps_daily, expected_annual_pct, realized_pnl, opened_at, closed_at, note
		FROM positions
		WHERE status = 'OPEN' AND type = ANY($1)
		ORDER BY id`
	rows, err := s.pool.Query(ctx, q, types)
	if err != nil {
		return nil, fmt.Errorf("list open positions by type: %w", err)
	}
	defer rows.Close()
	var out []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.ID, &p.OpportunityID, &p.Type, &p.Status, &p.ExpectedNetBps,
			&p.ExpectedEntrySpreadBps, &p.ExpectedSwapBpsDaily, &p.ExpectedAnnualPct,
			&p.RealizedPnl, &p.OpenedAt, &p.ClosedAt, &p.Note); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
