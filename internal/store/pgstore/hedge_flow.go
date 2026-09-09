// D-212 对冲工作台：跨源聚合 store 方法（SSOT §2/A4）。
// SumCMCOvernightByRange（fin_cmc_history 隔夜息之和）+ ListHedgeFlowPage（UNION ALL 跨源分页）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// HedgeFlowItem 对应统一流水卡单行（UNION ALL 跨源）。
// Source='trx'|'cmc'；Kind 归一 8 类（rental_income/vote_claim/deposit/withdraw/stake/unstake/overnight/other）；
// AmountValue 原生单位（TRX 笔=sun÷1e6 的 TRX 数；CMC 笔=USD）；AmountUnit='TRX'|'USD'。
type HedgeFlowItem struct {
	Source       string // trx|cmc
	Ts           time.Time
	Kind         string  // 归一 8 类
	AmountValue  float64 // 原生单位数值
	AmountUnit   string  // TRX|USD
	Counterparty string  // TRX=对手方地址；CMC=空
	TxID         string  // TRX=tx_id；CMC=transaction_id
}

// SumCMCOvernightByRange D-212 A4：fin_cmc_history OVERNIGHT_SETTLEMENT 之和（USD）[from,to]。
// from/to 为 nil = 不设界（全量）。
func (s *Store) SumCMCOvernightByRange(ctx context.Context, from, to *time.Time) (float64, error) {
	var fromTs, toTs *time.Time
	if from != nil {
		v := from.UTC()
		fromTs = &v
	}
	if to != nil {
		v := to.UTC()
		toTs = &v
	}
	var sum float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount),0) FROM fin_cmc_history
		WHERE transaction_type = 'OVERNIGHT_SETTLEMENT'
		  AND ($1::timestamptz IS NULL OR transaction_time >= $1::timestamptz)
		  AND ($2::timestamptz IS NULL OR transaction_time < $2::timestamptz)
	`, fromTs, toTs).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum cmc overnight by range: %w", err)
	}
	return sum, nil
}

// ListHedgeFlowPage D-212 A4：UNION ALL 跨源分页（fin_trx_txs + fin_cmc_history）按 ts DESC。
// sourceFilter=” = 全部；'trx'/'cmc' = 单源。limit/offset 后端分页。
// kind 归一 8 类：TRX 原样（已是 7 类）；CMC OVERNIGHT_SETTLEMENT→overnight、PAYMENTS_IN→deposit，其余→other。
// R1 灰尘过滤（业主裁定）：TRX 转入类（deposit/rental_income/other）amount_sun < 10000（<0.01 TRX）不返回——
// 灰尘广告转账过滤；仅查询过滤，入库保留（账本完整性，D-211 采集器不动）。CMC 行不受影响。
// R2（9-9 业主问询修复）：①PAYMENTS_IN→deposit（入金 $6,000 误显「其他」）②SELL_TRADE/BUY_TRADE 不进流水卡——
// CFD 开平仓零现金流（生产实证 amount=0），流水卡职责=资金流；开仓事件由对冲卡空单腿展示，入库保留。
func (s *Store) ListHedgeFlowPage(ctx context.Context, sourceFilter string, limit, offset int) ([]HedgeFlowItem, error) {
	if limit <= 0 {
		limit = 50
	}
	// UNION ALL 两源；CMC kind 用 CASE 归一。sourceFilter 用 WHERE 子句过滤。
	// R1：TRX 腿加灰尘过滤 NOT (kind IN (deposit,rental_income,other) AND amount_sun < 10000)。
	// R2：CMC 腿排除 SELL_TRADE/BUY_TRADE（非资金流）。
	rows, err := s.pool.Query(ctx, `
		SELECT source, ts, kind, amount_value, amount_unit, counterparty, tx_id FROM (
			SELECT 'trx' AS source, ts, kind,
			       amount_sun / 1e6 AS amount_value, 'TRX' AS amount_unit,
			       COALESCE(counterparty,'') AS counterparty, tx_id
			FROM fin_trx_txs
			WHERE ($1 = '' OR $1 = 'trx')
			  AND NOT (kind IN ('deposit','rental_income','other') AND amount_sun < 10000)
			UNION ALL
			SELECT 'cmc' AS source, transaction_time AS ts,
			       CASE transaction_type
			         WHEN 'OVERNIGHT_SETTLEMENT' THEN 'overnight'
			         WHEN 'PAYMENTS_IN' THEN 'deposit'
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
