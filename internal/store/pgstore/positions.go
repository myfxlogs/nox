package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrTerminalState 持仓已是终态（CLOSED/FAILED），不可回退（D-077 修复⑩ G4）。
var ErrTerminalState = errors.New("position is in terminal state (CLOSED/FAILED), cannot update")

// ErrClaimFailed 原子领取平仓权失败（position 非 OPEN/LEGGED 态，已被领取或非可平）。
// D-159 W1a R3-1：ClaimClose 只允许 OPEN/LEGGED→CLOSING，其他状态（OPENING/CLOSING/CLOSED/FAILED）→ ErrClaimFailed。
var ErrClaimFailed = errors.New("position not in claimable state (OPEN/LEGGED), close claim failed")

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
	NotionalUSD            *float64 // D-179：该 position 全部腿名义合计（USD；SWAPARB=双腿=2×单腿）
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
// D-179 S2：notional_usd 列同步（p.NotionalUSD 非 nil 时落盘，nil=NULL 向后兼容）。
func (s *Store) CreatePosition(ctx context.Context, p *Position) (int64, error) {
	q := `INSERT INTO positions (opportunity_id, type, status, expected_net_bps, opened_at, notional_usd)
		VALUES ($1, $2, $3, $4, now(), $5) RETURNING id`
	var id int64
	err := s.pool.QueryRow(ctx, q, p.OpportunityID, p.Type, p.Status, p.ExpectedNetBps, p.NotionalUSD).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create position: %w", err)
	}
	return id, nil
}

// UpdatePositionNotional D-179 S2：写入 notional_usd（Execute 闭包创建 position 后补写双腿口径）。
func (s *Store) UpdatePositionNotional(ctx context.Context, id int64, notional float64) error {
	_, err := s.pool.Exec(ctx, `UPDATE positions SET notional_usd = $1 WHERE id = $2`, notional, id)
	return err
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

// ClaimClose D-159 W1a R3-1：原子领取平仓权。
// 只有 OPEN/LEGGED → CLOSING 成功（RowsAffected=1）；其他状态（OPENING/CLOSING/CLOSED/FAILED）→ ErrClaimFailed。
// 防并发对账+手动平仓重复领取：CLOSING→CLOSING 被拒（只允许从 OPEN/LEGGED 进入 CLOSING）。
func (s *Store) ClaimClose(ctx context.Context, id int64) error {
	q := `UPDATE positions SET status = 'CLOSING' WHERE id = $1 AND status IN ('OPEN','LEGGED')`
	ct, err := s.pool.Exec(ctx, q, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrClaimFailed
	}
	return nil
}

// SetPositionUncertainty D-159 W1a R2：写 uncertainty_reason（UNKNOWN 过渡语义）。
// 与 UpdatePositionStatus 同事务语义（终态不可回退），单独方法避免改 UpdatePositionStatus 签名。
// reason 空 = 清除（恢复确认态时用）。
func (s *Store) SetPositionUncertainty(ctx context.Context, id int64, reason string) error {
	q := `UPDATE positions SET uncertainty_reason = $1 WHERE id = $2 AND status NOT IN ('CLOSED','FAILED')`
	_, err := s.pool.Exec(ctx, q, reason, id)
	return err
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

// ClosePosition 平仓回填（D-159 W1a R3-1 原子领取）。
// WHERE status NOT IN ('CLOSED','FAILED') 守卫：终态不可回退，并发对账+手动平仓重复调用
// 只第一次生效（RowsAffected=0 返回 ErrTerminalState），防重复 CLOSED + 重复 realized_pnl 覆盖。
func (s *Store) ClosePosition(ctx context.Context, id int64, realizedPnl float64) error {
	// D-070 修复：positions 表无 updated_at 列（D-060 同类 bug），去掉 updated_at = now()
	q := `UPDATE positions SET status = 'CLOSED', realized_pnl = $1, closed_at = now() WHERE id = $2 AND status NOT IN ('CLOSED','FAILED')`
	ct, err := s.pool.Exec(ctx, q, realizedPnl, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrTerminalState
	}
	return nil
}

// CreatePositionLeg 写入单腿记录（D-159 W1a R3-1 原子领取防重）。
// ON CONFLICT (position_id, leg_no, phase) DO NOTHING：并发对账+手动平仓重复写 CLOSE leg 时
// 只第一次生效（返回 ErrTerminalState 表示已存在），防重复 CLOSE leg + 重复 cashflow。
func (s *Store) CreatePositionLeg(ctx context.Context, leg *PositionLeg) error {
	q := `INSERT INTO position_legs (position_id, leg_no, phase, venue, account, symbol, side, qty,
		price_expected, price_actual, order_id, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (position_id, leg_no, phase) DO NOTHING`
	ct, err := s.pool.Exec(ctx, q,
		leg.PositionID, leg.LegNo, leg.Phase, leg.Venue, leg.Account, leg.Symbol, leg.Side,
		leg.Qty, leg.PriceExpected, leg.PriceActual, leg.OrderID, leg.Status)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrTerminalState
	}
	return nil
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

// ListPositionLegsByPhase D-159 W2a：按 phase 过滤查腿（残量校验用，只查 CLOSE phase 腿）。
// 复用 ListPositionLegs 的 SELECT 列，加 WHERE phase = $2 过滤。
func (s *Store) ListPositionLegsByPhase(ctx context.Context, posID int64, phase string) ([]PositionLeg, error) {
	q := `SELECT id, position_id, leg_no, phase, venue, account, symbol, side, qty,
		price_expected, price_actual, order_id, status, ts
		FROM position_legs WHERE position_id = $1 AND phase = $2 ORDER BY leg_no`
	rows, err := s.pool.Query(ctx, q, posID, phase)
	if err != nil {
		return nil, fmt.Errorf("list legs by phase: %w", err)
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

// SetPositionNote D-159 W2a：更新 position.note 字段（settlement 失败标记 settle_failed 用）。
// note 字段已存在（无 schema 变更），用于标记可重试状态供下轮 retry。
func (s *Store) SetPositionNote(ctx context.Context, id int64, note string) error {
	_, err := s.pool.Exec(ctx, `UPDATE positions SET note = $1 WHERE id = $2`, note, id)
	return err
}
