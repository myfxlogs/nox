// exec_face_symbols.go —— D-663 分层报价：indiv 执行面集合源。
// 集合=armed 组合腿 ∪ 毕业组合腿（carry_graduations approved→combo_id）∪
// 在仓 open 腿——限 binance venue，返 stream 级 venueType（spot/perp）划分。
// indiv 集与执行面查询同源同时刻（审项 I 集合同源对拍锚）。
package pgstore

import (
	"context"

	"fmt"
	"nox/internal/data/keydomain"
	"strings"
)

// ExecFaceVenueSymbols 执行面指定 venue 腿集（map[streamSym]venueType——
// D-665 泛化：okx/gate 同律收窄）。perp 优先（执行腿鲜度需求强）。
func (s *Store) ExecFaceVenueSymbols(ctx context.Context, venue string) (map[string]string, error) {
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
	add := func(canonical, v, vt string) {
		if v != venue || canonical == "" {
			return
		}
		sym := strings.ToLower(keydomain.MarketCanonical(canonical)) // 残⑤方言归一（XAG_USDT→xagusdt——canonical 第四形态坑）
		if vt != "spot" {                                            // perp/futures/delivery 全归 perp 流面（bookTicker=永续）
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
		WHERE venue_key=$1 AND phase='open'`, venue)
	if err != nil {
		return nil, fmt.Errorf("exec_face open legs: %w", err)
	}
	defer legRows.Close()
	for legRows.Next() {
		var sym, vt string
		if err := legRows.Scan(&sym, &vt); err != nil {
			return nil, err
		}
		add(strings.ToUpper(sym), venue, vt)
	}
	return out, legRows.Err()
}

// ExecFaceBinanceSymbols 执行面 binance 腿集（D-663 原签名沿——D-665 泛化后
// 薄壳兼容器）。
func (s *Store) ExecFaceBinanceSymbols(ctx context.Context) (map[string]string, error) {
	return s.ExecFaceVenueSymbols(ctx, "binance")
}

// MonitorFaceSymbols xasset-monitor 观测集（fin_carry_universe **达标行**——
// derive_inputs.go 同阈：structure 两型+note 空+net∈[minNet,maxNet] 参化
// 单源〔调用方传 scan.PaperCfdOpenNetPct/PaperCfdMaxNetPct——返修②字面量
// 双源修〕）。与执行面集分层：订阅占额序=exec 先、monitor 填余
// （armed/在仓腿不被监控集挤掉=返修①优先占额修）。
func (s *Store) MonitorFaceSymbols(ctx context.Context, minNet, maxNet float64) (map[string]string, error) {
	// 返修④：canonical→venue 原生档名单源映射——carry canonical=资产
	// canonical（SILVERUSDT/WTIUSDT 形），venue 档名经
	// fin_cex_universe.hedge_canonical 同键联查（monitor CexNativeSym 径
	// 消费面同域）；无档行=该 venue 未上市如实不出（零订阅不误造）。
	rows, err := s.pool.Query(ctx, `
		SELECT u.symbol, cu.cex_venue FROM fin_carry_universe cu
		JOIN fin_cex_universe u
		  ON u.venue = cu.cex_venue AND (
		     u.hedge_canonical IN (upper(cu.canonical), upper(cu.canonical)||'USDT')
		     OR regexp_replace(upper(u.symbol),'[^A-Z0-9]','','g') = upper(cu.canonical)||'USDT')
		WHERE cu.structure IN ('long_cfd_short_perp','short_cfd_long_perp')
		  AND cu.note = '' AND cu.net_annual_pct >= $1 AND cu.net_annual_pct <= $2`, minNet, maxNet)
	if err != nil {
		return nil, fmt.Errorf("monitor_face universe: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var nat, venue string
		if err := rows.Scan(&nat, &venue); err != nil {
			return nil, err
		}
		if venue == "gate" || venue == "okx" || venue == "binance" {
			out[strings.ToLower(keydomain.MarketCanonical(nat))] = venue // 原生档名→canonical 键形同形归一
		}
	}
	return out, rows.Err()
}
