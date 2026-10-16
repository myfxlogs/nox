// dci_settle.go —— D-207：DCI 交割观察 store 面。
// 观察=扫描拍内 ListFilledDCIOrders→各解析 response.settleDate（回执原值不估算）；
// 确认终态→SettleDCIOrder CAS 翻 settled 记 exercisedCoin+本息；
// OutstandingDCIQty 窗口同改按 response.settleDate 截（替 created+tenor 估算，
// 估算有 +4h 过冲实证）；缺失/不可解析回执降回估算窗（如实降级不猜）。
package pgstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// FilledDCIOrder 交割观察行——response 原值携带由观察方解析 settleDate。
type FilledDCIOrder struct {
	ID         int64
	RequestID  string
	Venue      string
	ProductRef string
	OptType    string
	InvestCcy  string
	Underlying string
	Qty        float64
	TenorDays  *int
	ProposalID *int64 // D-258 M4.1④：结算回写实际 APR 锚
	Response   string
}

// ListFilledDCIOrders 交割观察候选集（filled 非终态——settled 才是；行少量可全取）。
func (s *Store) ListFilledDCIOrders(ctx context.Context) ([]FilledDCIOrder, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, request_id, venue, product_ref, opt_type, invest_ccy,
		       COALESCE(underlying,''), COALESCE(qty,0), tenor_days, proposal_id,
		       COALESCE(response,'')
		FROM fin_dci_order WHERE status='filled' AND opt_type<>'EARN' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list filled dci orders: %w", err)
	}
	defer rows.Close()
	var out []FilledDCIOrder
	for rows.Next() {
		var o FilledDCIOrder
		if err := rows.Scan(&o.ID, &o.RequestID, &o.Venue, &o.ProductRef, &o.OptType,
			&o.InvestCcy, &o.Underlying, &o.Qty, &o.TenorDays, &o.ProposalID,
			&o.Response); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SettleDCIOrder 交割翻态 CAS——filled→settled 记 venue 实拍行权方向+本息额。
// ok=false=已被并发收口/态漂移（如实不报假 settled）。
func (s *Store) SettleDCIOrder(ctx context.Context, id int64, ccy string, qty, income float64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE fin_dci_order SET status='settled', settle_ccy=$2,
		       settle_qty=$3, settle_income=$4, settled_at=now()
		WHERE id=$1 AND status='filled'`, id, ccy, qty, income)
	if err != nil {
		return false, fmt.Errorf("settle dci order %d: %w", id, err)
	}
	return tag.RowsAffected() > 0, nil
}

// ClosePersonalPositionsByMarker D-207②：DCI 表象行同帧收口——note 含
// 执行单号标记（reqID）的 OPEN 行翻 CLOSED 并尾注收口因。
func (s *Store) ClosePersonalPositionsByMarker(ctx context.Context, marker, suffix string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE personal_positions SET status='CLOSED', closed_at=now(),
		       updated_at=now(), note=note||' → '||$2
		WHERE status='OPEN' AND kind='other' AND note LIKE '%'||$1||'%'`,
		marker, suffix)
	if err != nil {
		return 0, fmt.Errorf("close personal positions by marker: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ExchangeConfigID exchange_configs.id 反解（income_events 账户身份锚——
// NULL config_id 下唯一键不生效会重灌收益行，缺配置如实跳账）。
func (s *Store) ExchangeConfigID(ctx context.Context, venue, venueType string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM exchange_configs WHERE venue=$1 AND venue_type=$2
		ORDER BY id LIMIT 1`, venue, venueType).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return 0, nil
		}
		return 0, err
	}
	return id, nil
}

// SumDCISettleWindow 残差对账补腿（10-15 实证：DCI 到期还本离 earn 桶 → -358
// critical 无可归因项）：本拍窗内 settled 行——outflow=Σ qty WHERE
// invest_ccy=ccy（本金离 earn 池）；inflow=Σ settle_qty WHERE settle_ccy=ccy
// （本息到 venue 默认钱包）。不回溯——只认窗内 settled_at。
func (s *Store) SumDCISettleWindow(ctx context.Context, venue, ccy string, from, to time.Time) (outflow, inflow float64, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(qty) FILTER (WHERE invest_ccy=$2), 0),
		       COALESCE(SUM(settle_qty) FILTER (WHERE settle_ccy=$2), 0)
		FROM fin_dci_order
		WHERE venue=$1 AND status='settled' AND settled_at > $3 AND settled_at <= $4`,
		venue, ccy, from, to).Scan(&outflow, &inflow)
	if err != nil {
		return 0, 0, fmt.Errorf("dci settle window: %w", err)
	}
	return outflow, inflow, nil
}

// SumDCIFilledWindow 申购腿（10-16 实证：fin_dci_order#32 EARN 申购 351.64
// spot→earn 池间搬移炸双侧 critical——filled 申购在 io/划转/收益任一面都无行）。
// 本拍窗内翻 filled 的订单（含其后 settled——resolved_at=成交时刻不随交割改写；
// 个别径 resolved_at NULL 降回 created_at 不猜）：earnQty=opt_type='EARN' 申购额、
// dciQty=双币申购额（本金由源池搬进 earn 桶，双侧对称解释坍缩池间搬移残差）。
// 不回溯——只认窗内 COALESCE(resolved_at,created_at)。
func (s *Store) SumDCIFilledWindow(ctx context.Context, venue, ccy string, from, to time.Time) (earnQty, dciQty float64, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(qty) FILTER (WHERE opt_type='EARN'), 0),
		       COALESCE(SUM(qty) FILTER (WHERE opt_type<>'EARN'), 0)
		FROM fin_dci_order
		WHERE venue=$1 AND invest_ccy=$2 AND status IN ('filled','settled')
		  AND COALESCE(resolved_at, created_at) > $3
		  AND COALESCE(resolved_at, created_at) <= $4`,
		venue, ccy, from, to).Scan(&earnQty, &dciQty)
	if err != nil {
		return 0, 0, fmt.Errorf("dci filled window: %w", err)
	}
	return earnQty, dciQty, nil
}

// SumDisposalProceedsWindow 处置回款腿（10-15 实证：接货 BTC 市价卖回 +351.59
// USDT 落现货池，台账只有损益行——得额≠损益）。income_events
// dci_exercise_disposal 行（amount_usd=净损益，tran_id=orderID*10+3 同帧 /+7
// 清扫——后缀位=处置型标记）join 原单 qty=申购付额 → 得额=qty+pnl 台账真值
// 合成。损益行 symbol=标的币不入 SumIncomeForPool 池币种域，无重复计。
// 未锚定清扫行（anchor 缺不记 income）无本项——残差照响=自披露正确。
func (s *Store) SumDisposalProceedsWindow(ctx context.Context, venue, venueType string, from, to time.Time) (float64, error) {
	var v float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(o.qty + ie.amount_usd), 0)
		FROM income_events ie
		JOIN fin_dci_order o ON o.id = (ie.tran_id - MOD(ie.tran_id, 10)) / 10
		WHERE ie.venue=$1 AND ie.venue_type=$2
		  AND ie.income_type='dci_exercise_disposal'
		  AND MOD(ie.tran_id, 10) IN (3, 7)
		  AND ie.ts > $3 AND ie.ts <= $4`,
		venue, venueType, from, to).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("disposal proceeds window: %w", err)
	}
	return v, nil
}
