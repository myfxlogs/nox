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
	ID                int64
	OpportunityID     string
	Type              string
	Status            string
	ExpectedNetBps    float64
	ExpectedAnnualPct *float64 // D-080 S3：承诺年化%（funding 直存 / SWAPARB·CARRY=net_bps×365/100 / 收敛型 NULL）
	RealizedPnl       *float64
	OpenedAt          *time.Time
	ClosedAt          *time.Time
	Note              string
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
func (s *Store) CreatePosition(ctx context.Context, p *Position) (int64, error) {
	q := `INSERT INTO positions (opportunity_id, type, status, expected_net_bps)
		VALUES ($1, $2, $3, $4) RETURNING id`
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

// UpdatePositionMeta 回填持仓元数据（D-070：pipeline.Execute 创建时未填 type/expected_net_bps）。
// D-080 S3：同步写 expected_annual_pct（按型换算：funding 直存 net_bps / SWAPARB·CARRY=net_bps×365/100 / 收敛型 NULL）。
func (s *Store) UpdatePositionMeta(ctx context.Context, id int64, posType string, expectedNetBps float64) error {
	var annualPct any
	switch posType {
	case "FUNDING_HEDGE", "PERP_FUNDING":
		annualPct = expectedNetBps
	case "SWAPARB", "CARRY":
		v := expectedNetBps * 365 / 100
		annualPct = v
	default:
		annualPct = nil // 收敛型不适用年化
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE positions SET type = $1, expected_net_bps = $2, expected_annual_pct = $3 WHERE id = $4`,
		posType, expectedNetBps, annualPct, id)
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
	q := `SELECT id, opportunity_id, type, status, expected_net_bps, expected_annual_pct, realized_pnl, opened_at, closed_at, note
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
			&p.ExpectedAnnualPct, &p.RealizedPnl, &p.OpenedAt, &p.ClosedAt, &p.Note); err != nil {
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
	q := `SELECT id, opportunity_id, type, status, expected_net_bps, expected_annual_pct, realized_pnl, opened_at, closed_at, note
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
			&p.ExpectedAnnualPct, &p.RealizedPnl, &p.OpenedAt, &p.ClosedAt, &p.Note); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// HasCashflowForDay 检查指定持仓在某日是否已有某 kind 的现金流行（D-077 幂等：当日已记跳过）。
// day 为 UTC 日期（0 时），匹配该日 00:00~23:59:59.999 的行。
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
