package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Position 对应 positions 表（§2.11）。
type Position struct {
	ID             int64
	OpportunityID  string
	Type           string
	Status         string
	ExpectedNetBps float64
	RealizedPnl    *float64
	OpenedAt       *time.Time
	ClosedAt       *time.Time
	Note           string
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
func (s *Store) UpdatePositionStatus(ctx context.Context, id int64, status string) error {
	q := `UPDATE positions SET status = $1, updated_at = now() WHERE id = $2`
	_, err := s.pool.Exec(ctx, q, status, id)
	return err
}

// ClosePosition 平仓回填。
func (s *Store) ClosePosition(ctx context.Context, id int64, realizedPnl float64) error {
	q := `UPDATE positions SET status = 'CLOSED', realized_pnl = $1, closed_at = now(), updated_at = now()
		WHERE id = $2`
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
	q := `SELECT id, opportunity_id, type, status, expected_net_bps, realized_pnl, opened_at, closed_at, note
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
			&p.RealizedPnl, &p.OpenedAt, &p.ClosedAt, &p.Note); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
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
