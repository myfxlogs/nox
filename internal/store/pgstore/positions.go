package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arb/internal/keydomain"

	"github.com/jackc/pgx/v5"
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
	NotionalUSD            *float64   // D-179：该 position 全部腿名义合计（USD；SWAPARB=双腿=2×单腿）
	ClosingStartedAt       *time.Time // D-189 P0-4：进入 CLOSING 的真实起始时间（reconcile 超时判定用）
	UncertaintyReason      *string    // D-189 P0-4：UNKNOWN 标记（CLOSING+uncertainty_reason IS NOT NULL）
	EntryNoiseBps          *float64   // D-341 S1：开仓时噪声（学习样本锚；S2 止损带外化判距用）
	EntryHarvestFloorBps   *float64   // D-341 S1：开仓时收割线（学习样本锚）
	RequestID              string     // D-195-B：执行意图唯一 ID（migration 0048 列，本包开始写入）
	DedupKey               string     // D-195-B：业务组合防重（migration 0048 列，本包开始写入）
	Environment            string     // D-195-B：执行环境 demo/live（migration 0048 列，本包开始写入）
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
	Ts              time.Time
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
func (s *Store) CreatePosition(ctx context.Context, p *Position) (int64, error) {
	var reqID, dedupKey, env interface{}
	if p.RequestID != "" {
		reqID = p.RequestID
	}
	if p.DedupKey != "" {
		dedupKey = p.DedupKey
	}
	if p.Environment != "" {
		env = p.Environment
	}
	q := `INSERT INTO positions (opportunity_id, type, status, expected_net_bps, opened_at, notional_usd,
			request_id, dedup_key, environment)
		VALUES ($1, $2, $3, $4, now(), $5, $6, $7, $8) RETURNING id`
	var id int64
	err := s.pool.QueryRow(ctx, q, p.OpportunityID, p.Type, p.Status, p.ExpectedNetBps, p.NotionalUSD,
		reqID, dedupKey, env).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create position: %w", err)
	}
	s.notifyPositionChange() // D-469b S3
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
// D-189 P0-4：进入 CLOSING 时写 closing_started_at=COALESCE(closing_started_at, now())（保留首次时间，ResumeClose 重入不覆盖）。
func (s *Store) UpdatePositionStatus(ctx context.Context, id int64, status string) error {
	// D-060 实施修正②：去掉 updated_at——positions 表无此列，原 SQL 恒报错且被调用方
	// `_ =` 吞掉 → 所有 position 状态转换静默失败（position #2 卡 OPENING 实证）。
	var q string
	if status == "CLOSING" {
		q = `UPDATE positions SET status = $1, closing_started_at = COALESCE(closing_started_at, now()) WHERE id = $2 AND status NOT IN ('CLOSED','FAILED')`
	} else {
		q = `UPDATE positions SET status = $1 WHERE id = $2 AND status NOT IN ('CLOSED','FAILED')`
	}
	ct, err := s.pool.Exec(ctx, q, status, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrTerminalState
	}
	s.notifyPositionChange() // D-469b S3
	return nil
}

// ClaimClose D-159 W1a R3-1：原子领取平仓权。
// 只有 OPEN/LEGGED → CLOSING 成功（RowsAffected=1）；其他状态（OPENING/CLOSING/CLOSED/FAILED）→ ErrClaimFailed。
// 防并发对账+手动平仓重复领取：CLOSING→CLOSING 被拒（只允许从 OPEN/LEGGED 进入 CLOSING）。
// D-189 P0-4：进入 CLOSING 时写 closing_started_at=COALESCE(closing_started_at, now())（保留首次时间）。
func (s *Store) ClaimClose(ctx context.Context, id int64) error {
	q := `UPDATE positions SET status = 'CLOSING', closing_started_at = COALESCE(closing_started_at, now()) WHERE id = $1 AND status IN ('OPEN','LEGGED')`
	ct, err := s.pool.Exec(ctx, q, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrClaimFailed
	}
	s.notifyPositionChange() // D-469b S3
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
//   - CARRY：NetBps=年化百分点（同 FUNDING_HEDGE/PERP_FUNDING），消费方 verification.ExpectedUsd CARRY 分支
//     读 netBps/100×notional×holdDays/365；AnnualPct=NetBps（D-256-A S3 落值=netSwap×365/100）
//   - FUNDING_HEDGE/PERP_FUNDING/XASSET_CARRY：NetBps=年化百分点 / AnnualPct=NetBps（D-332 +XASSET，归 FUNDING 直存非 CARRY 折算）
type PositionMeta struct {
	EntrySpreadBps       *float64
	SwapBpsDaily         *float64
	NetBps               *float64
	AnnualPct            *float64
	EntryNoiseBps        *float64 // D-341 S1：开仓时噪声（学习样本锚）
	EntryHarvestFloorBps *float64 // D-341 S1：开仓时收割线（学习样本锚）
}

// UpdateExpectedEntrySpreadBps D-180 S3：写入 expected_entry_spread_bps（双腿成交价差基准）。
// Execute 闭包双腿成交后调用；存量活跃仓回填也用此方法。
// 已有值不覆写（回填幂等：WHERE expected_entry_spread_bps IS NULL）。
func (s *Store) UpdateExpectedEntrySpreadBps(ctx context.Context, id int64, bps float64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE positions SET expected_entry_spread_bps = $1 WHERE id = $2 AND expected_entry_spread_bps IS NULL`,
		bps, id)
	return err
}

// ClosePosition 平仓回填（D-159 W1a R3-1 原子领取）。
// WHERE status NOT IN ('CLOSED','FAILED') 守卫：终态不可回退，并发对账+手动平仓重复调用
// 只第一次生效（RowsAffected=0 返回 ErrTerminalState），防重复 CLOSED + 重复 realized_pnl 覆盖。
// D-599 S2：realized_pnl 语义=净额（价差−fees_usd）；fees_usd/feeQuality 同原子写入
// （feesUSD=nil+quality="none"=无费信息，NULL 不填 0）。
func (s *Store) ClosePosition(ctx context.Context, id int64, realizedPnl float64, feesUSD *float64, feeQuality string) error {
	// D-070 修复：positions 表无 updated_at 列（D-060 同类 bug），去掉 updated_at = now()
	q := `UPDATE positions SET status = 'CLOSED', realized_pnl = $1, fees_usd = $2, fee_quality = NULLIF($3,''), closed_at = now() WHERE id = $4 AND status NOT IN ('CLOSED','FAILED')`
	ct, err := s.pool.Exec(ctx, q, realizedPnl, feesUSD, feeQuality, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrTerminalState
	}
	s.notifyPositionChange() // D-469b S3
	return nil
}

// CreatePositionLeg 写入单腿记录（D-159 W1a R3-1 原子领取防重）。
// ON CONFLICT (position_id, leg_no, phase) DO NOTHING：并发对账+手动平仓重复写 CLOSE leg 时
// 只第一次生效（返回 ErrTerminalState 表示已存在），防重复 CLOSE leg + 重复 cashflow。
func (s *Store) CreatePositionLeg(ctx context.Context, leg *PositionLeg) error {
	vk := keydomain.Parse(leg.Venue) // D-496：leg.Venue 携复合键，落库拆裸键+通道型
	q := `INSERT INTO position_legs (position_id, leg_no, phase, attempt_no, venue, venue_type, symbol, side, qty,
		price_expected, price_actual, order_id, status,
		parent_attempt_id, client_order_id, requested_qty, filled_qty, avg_fill_price,
		fee_raw, fee_usd, submitted_at, uncertain_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		ON CONFLICT (position_id, leg_no, phase, attempt_no) DO NOTHING`
	ct, err := s.pool.Exec(ctx, q,
		leg.PositionID, leg.LegNo, leg.Phase, leg.AttemptNo, vk.Venue, vk.Type, leg.Symbol, leg.Side,
		leg.Qty, leg.PriceExpected, leg.PriceActual, leg.OrderID, leg.Status,
		leg.ParentAttemptID, leg.ClientOrderID, leg.RequestedQty, leg.FilledQty, leg.AvgFillPrice,
		leg.FeeRaw, leg.FeeUSD, leg.SubmittedAt, leg.UncertainAt)
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

// SetPositionNote D-159 W2a：更新 position.note 字段（settlement 失败标记 settle_failed 用）。
// note 字段已存在（无 schema 变更），用于标记可重试状态供下轮 retry。
func (s *Store) SetPositionNote(ctx context.Context, id int64, note string) error {
	_, err := s.pool.Exec(ctx, `UPDATE positions SET note = $1 WHERE id = $2`, note, id)
	return err
}

// GetPositionByRequestID D-195-B §4.1：request_id 幂等查询（重复提交返回已有 position）。
// 不存在 → 返回 (nil, nil)（调用方按"新建"处理）。
func (s *Store) GetPositionByRequestID(ctx context.Context, requestID string) (*Position, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, opportunity_id, type, status, COALESCE(expected_net_bps, 0), opened_at, closed_at, note,
		       notional_usd, request_id, dedup_key, environment
		FROM positions WHERE request_id = $1`, requestID)
	var p Position
	var reqID, dedupKey, env *string
	err := row.Scan(&p.ID, &p.OpportunityID, &p.Type, &p.Status, &p.ExpectedNetBps, &p.OpenedAt, &p.ClosedAt, &p.Note,
		&p.NotionalUSD, &reqID, &dedupKey, &env)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get position by request_id: %w", err)
	}
	if reqID != nil {
		p.RequestID = *reqID
	}
	if dedupKey != nil {
		p.DedupKey = *dedupKey
	}
	if env != nil {
		p.Environment = *env
	}
	return &p, nil
}
