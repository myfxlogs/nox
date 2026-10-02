// positions_legs.go —— D-018 阶段b：position_legs 域读写（struct+CRUD；D-013 fill_kind/
// commission 现金流接线后超 300 行契约，自 positions.go 拆出；scan 面=positions_legs_scan.go）。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"arb/internal/keydomain"
)

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
	// D-195-B §4.2：attempt 字段扩展。
	AttemptNo       int16    // 1=首次尝试；>1=重试
	ParentAttemptID *int64   // nil = 首次尝试；非 nil = 重试关联原 attempt
	ClientOrderID   string   // 确定性客户端订单号（venue 支持时携带）
	RequestedQty    *float64 // nil = 旧行兼容
	FilledQty       *float64
	AvgFillPrice    *float64
	FeeRaw          string // venue 原始手续费字符串
	FeeUSD          *float64
	SubmittedAt     *time.Time
	UncertainAt     *time.Time // status=UNKNOWN 时写入
	// FillKind D-018 阶段a：腿成交方式（"maker"|"taker"；NULL=旧数据/未采集，econ 回退 taker 保守口径）。
	FillKind *string
	Ts       time.Time
}

// EffectiveQty 实际持仓量（成交口径，时代稳定）：filled_qty 非空且>0 → 成交量
// （CEX=基础币，MT=手数）；否则回退 qty（pre-D-311 旧行 qty 本身即成交量）。
// qty 列=LegSpec 请求量，语义随时代分裂（CEX 旧行=基础币 / post-D-311 新行=合约数），
// 「实际持仓量」消费点一律用本方法（D-311 R6）。
func (l PositionLeg) EffectiveQty() float64 {
	if l.FilledQty != nil && *l.FilledQty > 0 {
		return *l.FilledQty
	}
	return l.Qty
}

// CreatePosition 建 position 记录，返回 ID。
// D-087 §3.3：opened_at = now()（holdDays 计算依赖，原 NULL 兜底 min(leg ts) 仍保留作冗余）。
// D-179 S2：notional_usd 列同步（p.NotionalUSD 非 nil 时落盘，nil=NULL 向后兼容）。
// D-195-B：request_id/dedup_key/environment 写入（空字符串→NULL，避免触发唯一约束）。

// CreatePositionLeg 写入单腿记录（D-159 W1a R3-1 原子领取防重）。
// ON CONFLICT (position_id, leg_no, phase) DO NOTHING：并发对账+手动平仓重复写 CLOSE leg 时
// 只第一次生效（返回 ErrTerminalState 表示已存在），防重复 CLOSE leg + 重复 cashflow。
func (s *Store) CreatePositionLeg(ctx context.Context, leg *PositionLeg) error {
	vk := keydomain.Parse(leg.Venue) // D-496：leg.Venue 携复合键，落库拆裸键+通道型
	q := `INSERT INTO position_legs (position_id, leg_no, phase, attempt_no, venue, venue_type, symbol, side, qty,
		price_expected, price_actual, order_id, status,
		parent_attempt_id, client_order_id, requested_qty, filled_qty, avg_fill_price,
		fee_raw, fee_usd, submitted_at, uncertain_at, fill_kind)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
		ON CONFLICT (position_id, leg_no, phase, attempt_no) DO NOTHING
		RETURNING id`
	var legID int64
	err := s.pool.QueryRow(ctx, q,
		leg.PositionID, leg.LegNo, leg.Phase, leg.AttemptNo, vk.Venue, vk.Type, leg.Symbol, leg.Side,
		leg.Qty, leg.PriceExpected, leg.PriceActual, leg.OrderID, leg.Status,
		leg.ParentAttemptID, leg.ClientOrderID, leg.RequestedQty, leg.FilledQty, leg.AvgFillPrice,
		leg.FeeRaw, leg.FeeUSD, leg.SubmittedAt, leg.UncertainAt, leg.FillKind).Scan(&legID)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return ErrTerminalState
		}
		return err
	}
	leg.ID = legID
	// D-013 ①：终态腿（FILLED/EXTERNAL）落库即写 leg:<id> commission 行，全写入点同源。
	return s.EnsureLegCommissionCashflow(ctx, *leg, nil)
}

// ListPositionLegs 返回指定持仓的全部腿。
func (s *Store) ListPositionLegs(ctx context.Context, posID int64) ([]PositionLeg, error) {
	q := `SELECT ` + legSelectColumns + ` FROM position_legs WHERE position_id = $1 ORDER BY leg_no`
	rows, err := s.pool.Query(ctx, q, posID)
	if err != nil {
		return nil, fmt.Errorf("list legs: %w", err)
	}
	defer rows.Close()
	return scanLegs(rows)
}

// ListPositionLegsByPhase D-159 W2a：按 phase 过滤查腿（残量校验用，只查 CLOSE phase 腿）。
// 复用 ListPositionLegs 的 SELECT 列，加 WHERE phase = $2 过滤。
func (s *Store) ListPositionLegsByPhase(ctx context.Context, posID int64, phase string) ([]PositionLeg, error) {
	q := `SELECT ` + legSelectColumns + ` FROM position_legs WHERE position_id = $1 AND phase = $2 ORDER BY leg_no`
	rows, err := s.pool.Query(ctx, q, posID, phase)
	if err != nil {
		return nil, fmt.Errorf("list legs by phase: %w", err)
	}
	defer rows.Close()
	return scanLegs(rows)
}
