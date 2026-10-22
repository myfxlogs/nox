// exec_face_symbols.go —— D-663 分层报价：indiv 执行面集合源。
// 集合=armed 组合腿 ∪ 毕业组合腿（carry_graduations approved→combo_id）∪
// 在仓 open 腿——限 binance venue，返 stream 级 venueType（spot/perp）划分。
// indiv 集与执行面查询同源同时刻（审项 I 集合同源对拍锚）。
package pgstore

import (
	"context"
	"fmt"
	"strings"
)

// ExecFaceBinanceSymbols 执行面 binance 腿集（map[streamSym]venueType——
// 两腿异面同 canonical 罕见但可能：perp 优先（执行腿鲜度需求强）。
func (s *Store) ExecFaceBinanceSymbols(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT canonical_buy, venue_buy, venue_type_buy, canonical_sell, venue_sell, venue_type_sell
		FROM xasset_allow_combos
		WHERE enabled AND (tier='armed'
		   OR id IN (SELECT combo_id FROM carry_graduations WHERE status='approved' AND combo_id IS NOT NULL))`)
	if err != nil {
		return nil, fmt.Errorf("exec_face armed: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	add := func(canonical, venue, vt string) {
		if venue != "binance" || canonical == "" {
			return
		}
		sym := strings.ToLower(canonical)
		if vt != "spot" { // perp/futures/delivery 全归 perp 流面（bookTicker=永续）
			if _, dup := out[sym]; !dup || out[sym] == "spot" {
				out[sym] = "perp"
			}
		} else if _, dup := out[sym]; !dup {
			out[sym] = "spot"
		}
	}
	for rows.Next() {
		var cb, vb, vtb, cs, vs, vts string
		if err := rows.Scan(&cb, &vb, &vtb, &cs, &vs, &vts); err != nil {
			return nil, err
		}
		add(cb, vb, vtb)
		add(cs, vs, vts)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 在仓 open 腿（position_legs venue_key='binance'、phase='open'——symbol
	// 原生 venue 形去尾缀到 canonical 域，vt 判 spot/perp）。
	legRows, err := s.pool.Query(ctx, `
		SELECT symbol, venue_type FROM position_legs
		WHERE venue_key='binance' AND phase='open'`)
	if err != nil {
		return nil, fmt.Errorf("exec_face open legs: %w", err)
	}
	defer legRows.Close()
	for legRows.Next() {
		var sym, vt string
		if err := legRows.Scan(&sym, &vt); err != nil {
			return nil, err
		}
		add(strings.ToUpper(sym), "binance", vt)
	}
	return out, legRows.Err()
}
