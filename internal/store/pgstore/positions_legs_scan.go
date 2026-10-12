package pgstore

import (
	"github.com/jackc/pgx/v5"
)

// legSelectColumns D-195-B：position_legs 统一 SELECT 列（含 attempt 字段）。
// D-496：venue 读 venue_key 生成列（对外仍复合键，调用方零改动）；account 死列已删，
// 结构字段保留恒 ”。
const legSelectColumns = `id, position_id, leg_no, phase, venue_key AS venue, ''::text AS account, symbol, side, qty,
		price_expected, price_actual, order_id, status,
		attempt_no, parent_attempt_id, client_order_id, requested_qty, filled_qty, avg_fill_price,
		fee_raw, fee_usd, submitted_at, uncertain_at, fill_kind, fail_reason, ts`

// scanLegs D-195-B：position_legs 行扫描公共函数（3 处 List 方法共用）。
func scanLegs(rows pgx.Rows) ([]PositionLeg, error) {
	var out []PositionLeg
	for rows.Next() {
		var l PositionLeg
		if err := rows.Scan(&l.ID, &l.PositionID, &l.LegNo, &l.Phase, &l.Venue, &l.Account,
			&l.Symbol, &l.Side, &l.Qty, &l.PriceExpected, &l.PriceActual, &l.OrderID, &l.Status,
			&l.AttemptNo, &l.ParentAttemptID, &l.ClientOrderID, &l.RequestedQty, &l.FilledQty, &l.AvgFillPrice,
			&l.FeeRaw, &l.FeeUSD, &l.SubmittedAt, &l.UncertainAt, &l.FillKind, &l.FailReason, &l.Ts); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
