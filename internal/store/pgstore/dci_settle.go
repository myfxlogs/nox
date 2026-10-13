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
)

// FilledDCIOrder 交割观察行——response 原值携带由观察方解析 settleDate。
type FilledDCIOrder struct {
	ID         int64
	RequestID  string
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
		SELECT id, request_id, product_ref, opt_type, invest_ccy,
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
		if err := rows.Scan(&o.ID, &o.RequestID, &o.ProductRef, &o.OptType,
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
