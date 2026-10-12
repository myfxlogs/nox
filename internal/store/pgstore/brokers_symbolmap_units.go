// broker_symbol_map 报价单位归一层（D-594）：price_scale/qty_scale 消费查询。
package pgstore

import (
	"context"
	"errors"

	"arb/internal/platform/notional"

	"github.com/jackc/pgx/v5"
)

// unitSensitiveSQL 单位敏感判定（D-594）：canonical 存在任一已标定且非 1 的 scale 行 →
// 该 canonical 是单位混存品种（铜型），其未标定行必须 fail-closed；纯 1.0 品种不受影响。
const unitSensitiveSQL = `EXISTS (
		SELECT 1 FROM broker_symbol_map m2
		WHERE m2.canonical = m.canonical
		  AND ((m2.price_scale IS NOT NULL AND m2.price_scale <> 1.0)
		    OR (m2.qty_scale IS NOT NULL AND m2.qty_scale <> 1.0))
	)`

// ListUnitSensitiveCanonicals 返回单位敏感 canonical 集（D-594）——任一标定行 scale≠1 的
// canonical。canonical 发射闸与 symdig unit_unknown 分类共用此判据。
func (s *Store) ListUnitSensitiveCanonicals(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT canonical FROM broker_symbol_map
		WHERE canonical <> ''
		  AND ((price_scale IS NOT NULL AND price_scale <> 1.0)
		    OR (qty_scale IS NOT NULL AND qty_scale <> 1.0))
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out[c] = true
	}
	return out, rows.Err()
}

// unitQty 单查询实现（UnitQtyByBrokerSymbol/UnitQtyByCanonical 共用）：
// 返回 contract_size × qty_scale = canonical 单位数/手。
// qty_scale NULL 且 canonical 单位敏感 → ok=false（未标定 fail-closed）；
// qty_scale NULL 且非敏感 → 按 1.0（新增非敏感行未标定不阻塞）。contract_size NULL → false。
func (s *Store) unitQty(ctx context.Context, where string, args ...any) (float64, bool, error) {
	var cs float64
	var qs *float64
	var sensitive bool
	// ORDER BY is_primary DESC：canonical 维度多行组（venue×canonical 多 broker_symbol）
	// 钉确定性取 primary 行（D-594 R2）；broker_symbol 维度行唯一，排序无影响。
	err := s.pool.QueryRow(ctx, `
		SELECT m.contract_size, m.qty_scale, `+unitSensitiveSQL+`
		FROM broker_symbol_map m WHERE m.contract_size IS NOT NULL AND `+where+`
		ORDER BY m.is_primary DESC LIMIT 1
	`, args...).Scan(&cs, &qs, &sensitive)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if qs == nil {
		if sensitive {
			return 0, false, nil
		}
		return cs, true, nil
	}
	return cs * *qs, true, nil
}

// UnitQtyByBrokerSymbol (venueKey, broker_symbol) → canonical 单位数/手（D-594 S3）。
// 供 canonical 价配套的名义额链消费（legContractSizeOf/报价池价配对）；
// PriceActual 原生价配对链继续用 ContractSizeByBrokerSymbol（原生 CS）。
func (s *Store) UnitQtyByBrokerSymbol(ctx context.Context, venueKey, brokerSymbol string) (float64, bool, error) {
	return s.unitQty(ctx, `m.venue = $1 AND m.broker_symbol = $2`,
		notional.VenueAccount(venueKey), brokerSymbol)
}

// UnitQtyByCanonical (venueKey, canonical) → canonical 单位数/手（D-594 S3）。
// 供机会卡 USD 派生（o.PriceBuy canonical 价）等同 canonical 口径消费。
func (s *Store) UnitQtyByCanonical(ctx context.Context, venueKey, canonical string) (float64, bool, error) {
	return s.unitQty(ctx, `m.venue = $1 AND m.canonical = $2`,
		notional.VenueAccount(venueKey), canonical)
}
