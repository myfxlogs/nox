package pgstore

// hedge_flow.go —— 统一流水读面（arb hedge_flow.go 同名件对拍，D-212）。
// UNION ALL 双源：fin_trx_txs（cheat_status 灰尘滤+vote 非资金流滤）+
// fin_cmc_history（transaction_type CASE 归一，SELL/BUY_TRADE 非资金流滤）。
import (
	"context"
	"fmt"
	"time"
)

// HedgeFlowItem 归一流水行（source=trx|cmc；amount 原生单位+unit 币种）。
type HedgeFlowItem struct {
	Source       string
	Ts           time.Time
	Kind         string
	AmountValue  float64
	AmountUnit   string
	Counterparty string
	TxID         string
}

// ListHedgeFlowPage sourceFilter 空=全部/trx/cmc 单源；分页 limit/offset。
func (s *Store) ListHedgeFlowPage(ctx context.Context, sourceFilter string, limit, offset int) ([]HedgeFlowItem, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT source, ts, kind, amount_value, amount_unit, counterparty, tx_id FROM (
			SELECT 'trx' AS source, ts, kind,
			       amount_sun / 1e6 AS amount_value, 'TRX' AS amount_unit,
			       COALESCE(counterparty,'') AS counterparty, tx_id
			FROM fin_trx_txs
			WHERE ($1 = '' OR $1 = 'trx')
			  AND NOT cheat_status
			  AND kind <> 'vote'
			UNION ALL
			SELECT 'cmc' AS source, transaction_time AS ts,
			       CASE transaction_type
			         WHEN 'OVERNIGHT_SETTLEMENT' THEN 'overnight'
			         WHEN 'PAYMENTS_IN' THEN 'deposit'
			         WHEN 'CLOSE_TRADE' THEN 'close_trade'
			         WHEN 'SELL_ORDER' THEN 'sell_order'
			         WHEN 'ORDER_CANCELLED' THEN 'order_cancelled'
			         ELSE 'other'
			       END AS kind,
			       amount AS amount_value, 'USD' AS amount_unit,
			       '' AS counterparty, transaction_id AS tx_id
			FROM fin_cmc_history
			WHERE ($1 = '' OR $1 = 'cmc')
			  AND transaction_type NOT IN ('SELL_TRADE','BUY_TRADE')
		) AS u
		ORDER BY ts DESC, tx_id DESC
		LIMIT $2 OFFSET $3
	`, sourceFilter, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list hedge flow page: %w", err)
	}
	defer rows.Close()
	var out []HedgeFlowItem
	for rows.Next() {
		var it HedgeFlowItem
		if err := rows.Scan(&it.Source, &it.Ts, &it.Kind, &it.AmountValue, &it.AmountUnit, &it.Counterparty, &it.TxID); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
