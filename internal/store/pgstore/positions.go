package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrTerminalState 持仓已是终态（CLOSED/FAILED），不可回退（D-077 修复⑩ G4）。
var ErrTerminalState = errors.New("position is in terminal state (CLOSED/FAILED), cannot update")

// Position 对应 positions 表（§2.11）。
type Position struct {
	ID                     int64
	OpportunityID          string
	Type                   string
	Status                 string
	ExpectedNetBps         float64  // 收敛型一次性 bps（SWAPARB 已挪至 ExpectedSwapBpsDaily，D-087）
	ExpectedEntrySpreadBps *float64 // D-087：SWAPARB 入场 spread（原 expected_net_bps 挪列）
	ExpectedSwapBpsDaily   *float64 // D-087：SWAPARB 日 swap bps
	ExpectedAnnualPct      *float64 // D-080 S3：承诺年化%（funding 直存 / SWAPARB·CARRY=net_bps×365/100 / 收敛型 NULL）
	RealizedPnl            *float64
	OpenedAt               *time.Time
	ClosedAt               *time.Time
	Note                   string
}

// PositionLeg 对应 position_legs 表（§2.11）。
type PositionLeg struct {
	ID            int64
	PositionID    int64
	LegNo         int16
	Phase         string
	Venue         string
	Account       string
	Symbol        string
	Side          string
	Qty           float64
	PriceExpected float64
	PriceActual   *float64
	OrderID       string
	Status        string
	Ts            time.Time
}

// CreatePosition 建 position 记录，返回 ID。
// D-087 §3.3：opened_at = now()（holdDays 计算依赖，原 NULL 兜底 min(leg ts) 仍保留作冗余）。
func (s *Store) CreatePosition(ctx context.Context, p *Position) (int64, error) {
	q := `INSERT INTO positions (opportunity_id, type, status, expected_net_bps, opened_at)
		VALUES ($1, $2, $3, $4, now()) RETURNING id`
	var id int64
	err := s.pool.QueryRow(ctx, q, p.OpportunityID, p.Type, p.Status, p.ExpectedNetBps).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create position: %w", err)
	}
	return id, nil
}

// UpdatePositionStatus 更新持仓状态。
// D-077 修复⑩ G4：终态不可逆守卫——CLOSED/FAILED 不可回退（WHERE NOT IN）。
// 返回 ErrTerminalState 表示 0 行受影响（已是终态，晚写拦截）。
func (s *Store) UpdatePositionStatus(ctx context.Context, id int64, status string) error {
	// D-060 实施修正②：去掉 updated_at——positions 表无此列，原 SQL 恒报错且被调用方
	// `_ =` 吞掉 → 所有 position 状态转换静默失败（position #2 卡 OPENING 实证）。
	q := `UPDATE positions SET status = $1 WHERE id = $2 AND status NOT IN ('CLOSED','FAILED')`
	ct, err := s.pool.Exec(ctx, q, status, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrTerminalState
	}
	return nil
}

// PositionMeta typed 持仓元数据（D-087 §3.4 字段语义分离）。
// 各型按语义给值，nil=该列不写（保持 NULL）：
//   - 收敛型（CROSS_VENUE/MEANREVERT）：NetBps=一次性 bps，其余 nil
//   - SWAPARB：EntrySpreadBps=入场 spread / SwapBpsDaily=日 swap / NetBps=nil / AnnualPct=SwapBpsDaily×365/100
//   - CARRY：NetBps=net bps / AnnualPct=NetBps×365/100
//   - FUNDING_HEDGE/PERP_FUNDING：NetBps=年化百分点 / AnnualPct=NetBps
type PositionMeta struct {
	EntrySpreadBps *float64
	SwapBpsDaily   *float64
	NetBps         *float64
	AnnualPct      *float64
}

// UpdatePositionMeta 回填持仓元数据（D-087 §3.4 typed 化）。
// 调用方按型构造 PositionMeta，本方法按字段写入（typed 列分离，不再混用 expected_net_bps）。
func (s *Store) UpdatePositionMeta(ctx context.Context, id int64, posType string, m PositionMeta) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE positions SET type = $1, expected_net_bps = $2, expected_entry_spread_bps = $3,
			expected_swap_bps_daily = $4, expected_annual_pct = $5 WHERE id = $6`,
		posType, m.NetBps, m.EntrySpreadBps, m.SwapBpsDaily, m.AnnualPct, id)
	return err
}

// UpdatePositionOppID 更新持仓的 opportunity_id（D-070 swap 套利用）。
func (s *Store) UpdatePositionOppID(ctx context.Context, id int64, oppID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE positions SET opportunity_id = $1 WHERE id = $2`, oppID, id)
	return err
}

// ClosePosition 平仓回填。
func (s *Store) ClosePosition(ctx context.Context, id int64, realizedPnl float64) error {
	// D-070 修复：positions 表无 updated_at 列（D-060 同类 bug），去掉 updated_at = now()
	q := `UPDATE positions SET status = 'CLOSED', realized_pnl = $1, closed_at = now() WHERE id = $2`
	_, err := s.pool.Exec(ctx, q, realizedPnl, id)
	return err
}

// CreatePositionLeg 写入单腿记录。
func (s *Store) CreatePositionLeg(ctx context.Context, leg *PositionLeg) error {
	q := `INSERT INTO position_legs (position_id, leg_no, phase, venue, account, symbol, side, qty,
		price_expected, price_actual, order_id, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`
	_, err := s.pool.Exec(ctx, q,
		leg.PositionID, leg.LegNo, leg.Phase, leg.Venue, leg.Account, leg.Symbol, leg.Side,
		leg.Qty, leg.PriceExpected, leg.PriceActual, leg.OrderID, leg.Status)
	return err
}

// ListPositionLegs 返回指定持仓的全部腿。
func (s *Store) ListPositionLegs(ctx context.Context, posID int64) ([]PositionLeg, error) {
	q := `SELECT id, position_id, leg_no, phase, venue, account, symbol, side, qty,
		price_expected, price_actual, order_id, status, ts
		FROM position_legs WHERE position_id = $1 ORDER BY leg_no`
	rows, err := s.pool.Query(ctx, q, posID)
	if err != nil {
		return nil, fmt.Errorf("list legs: %w", err)
	}
	defer rows.Close()
	var out []PositionLeg
	for rows.Next() {
		var l PositionLeg
		if err := rows.Scan(&l.ID, &l.PositionID, &l.LegNo, &l.Phase, &l.Venue, &l.Account,
			&l.Symbol, &l.Side, &l.Qty, &l.PriceExpected, &l.PriceActual, &l.OrderID, &l.Status, &l.Ts); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListPositions 分页返回持仓。
func (s *Store) ListPositions(ctx context.Context, status string, limit, offset int) ([]Position, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, opportunity_id, type, status, expected_net_bps, expected_entry_spread_bps, expected_swap_bps_daily, expected_annual_pct, realized_pnl, opened_at, closed_at, note
		FROM positions
		WHERE ($1 = '' OR status = $1)
		ORDER BY id DESC LIMIT $2 OFFSET $3`
	rows, err := s.pool.Query(ctx, q, status, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list positions: %w", err)
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

// CountPositions 返回符合筛选条件的持仓总数（D-069 分页控件用）。
func (s *Store) CountPositions(ctx context.Context, status string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM positions WHERE ($1 = '' OR status = $1)`, status).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count positions: %w", err)
	}
	return n, nil
}

// ListActivePositions 返回所有活跃态持仓（OPENING/OPEN/LEGGED/CLOSING）。
// D-080 R1：gate 防重改 DB 实时派生——替代内存 openCombos 集，治手动路径旁路。
// 行数=MaxPositions 量级，成本≈COUNT；返回行供 gate 派生 combo.FromOppID 匹配。
func (s *Store) ListActivePositions(ctx context.Context) ([]Position, error) {
	q := `SELECT id, opportunity_id, type, status, expected_net_bps, expected_entry_spread_bps, expected_swap_bps_daily, expected_annual_pct, realized_pnl, opened_at, closed_at, note
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
			&p.RealizedPnl, &p.OpenedAt, &p.ClosedAt, &p.Note); err != nil {
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
func (s *Store) HasOpenPositionByOpportunity(ctx context.Context, opportunityID string) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx,
		`SELECT 1 FROM positions WHERE opportunity_id = $1 AND status IN ('OPEN','OPENING','LEGGED') LIMIT 1`,
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
	q := `SELECT id, opportunity_id, type, status, expected_net_bps, expected_entry_spread_bps, expected_swap_bps_daily, expected_annual_pct, realized_pnl, opened_at, closed_at, note
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
