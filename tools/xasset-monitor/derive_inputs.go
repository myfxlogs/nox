// derive_inputs.go —— 派生簿入参层（D-472 S5→D-144 六查询+能力账本）：enabled 组合/
// TRADING universe/fin_carry_universe 达标行/active 纸面仓需求行/ticks 在流 MT 账户
// 枚举/asset_equiv/broker_symbol_map 能力账本（读面在 capabilities.go）。
// 自 derive.go 拆出=300 行棘轮（纯搬移零逻辑变更）。

package main

import (
	"context"
	"fmt"

	"nox/internal/research/scan"
	"nox/internal/store/pgstore"
)

// mtStreamWindow D-144：ticks 在流 MT 账户枚举窗——与 latestExnessTick 陈旧闸同值
// （10min；账户无新鲜 tick 时其簿对本就 skip，枚举窗取同口径不放大）。
const mtStreamWindow = "10 minutes"

// loadDerivationInputs D-472 S5→D-144：派生入参六查询——enabled 组合行/TRADING
// universe 行/fin_carry_universe 达标行/active 纸面仓需求行/ticks 在流 MT 账户枚举
// /asset_equiv（class 键实解析）+broker_symbol_map 能力账本（capabilities.go）。
func loadDerivationInputs(ctx context.Context, st *pgstore.Store) ([]allowComboRow, []universeRow, []cfdDemandRow, []mtAcct, []pgstore.AssetEquivEntry, map[string]map[string]bool, error) {
	var combos []allowComboRow
	// SELECT venue_key_* 生成列（D-496 键域权威口径）——裸列 venue_buy/sell
	// 在拆列后=裸号/裸名（"3001157"/"gate"），IsMTKey 全 false → mt×cex 组合误判同所
	// 拒采 → monitor 簿停写 49h（回归实证见 derive_test T3）。
	rows, err := st.Pool().Query(ctx, `SELECT canonical_buy, venue_key_buy, canonical_sell, venue_key_sell
		FROM xasset_allow_combos WHERE enabled = true OR tier = 'armed' ORDER BY id`)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("list combos: %w", err)
	}
	for rows.Next() {
		var c allowComboRow
		if err := rows.Scan(&c.canonicalBuy, &c.venueBuy, &c.canonicalSell, &c.venueSell); err != nil {
			rows.Close()
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("scan combo: %w", err)
		}
		combos = append(combos, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("combos rows: %w", err)
	}

	var uni []universeRow
	// 交割行剔监控簿（xasset 对=funding 腿永续面；FUTURES 行 WS 不订→ticks 零行
	// →每拍 critical 洪泛实证。NULL=存量 perp 语义收）。universe 采集面不动。
	uniRows, err := st.Pool().Query(ctx, `SELECT venue, symbol, hedge_canonical
		FROM fin_cex_universe WHERE status = 'TRADING'
		  AND COALESCE(contract_type,'PERP') <> 'FUTURES'`)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("list universe: %w", err)
	}
	for uniRows.Next() {
		var r universeRow
		if err := uniRows.Scan(&r.venue, &r.symbol, &r.hedgeCanonical); err != nil {
			uniRows.Close()
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("scan universe: %w", err)
		}
		uni = append(uni, r)
	}
	uniRows.Close()
	if err := uniRows.Err(); err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("universe rows: %w", err)
	}

	// 上游域①：fin_carry_universe 达标行（开仓域同阈，scan 常数单源）。
	carryRows, err := st.Pool().Query(ctx, `SELECT canonical, mt_venue, cex_venue
		FROM fin_carry_universe
		WHERE structure IN ('long_cfd_short_perp','short_cfd_long_perp')
		  AND note = '' AND net_annual_pct >= $1 AND net_annual_pct <= $2`,
		scan.PaperCfdOpenNetPct, scan.PaperCfdMaxNetPct)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("list carry universe: %w", err)
	}
	demandSet := map[string]bool{}
	var demand []cfdDemandRow
	for carryRows.Next() {
		var d cfdDemandRow
		if err := carryRows.Scan(&d.canonical, &d.mtVenue, &d.cexVenue); err != nil {
			carryRows.Close()
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("scan carry universe: %w", err)
		}
		k := d.mtVenue + "|" + d.cexVenue + "|" + d.canonical
		if !demandSet[k] {
			demandSet[k] = true
			demand = append(demand, d)
		}
	}
	carryRows.Close()
	if err := carryRows.Err(); err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("carry universe rows: %w", err)
	}

	// 上游域②：active cfd 纸面仓所需三元组（venue_a=mt 裸名/venue_b=cex 裸名）。
	paperRows, err := st.Pool().Query(ctx, `SELECT symbol, venue_a, venue_b
		FROM paper_spot_carry
		WHERE status = 'active' AND structure IN ('long_cfd_short_perp','short_cfd_long_perp')`)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("list active paper: %w", err)
	}
	for paperRows.Next() {
		var d cfdDemandRow
		if err := paperRows.Scan(&d.canonical, &d.mtVenue, &d.cexVenue); err != nil {
			paperRows.Close()
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("scan active paper: %w", err)
		}
		k := d.mtVenue + "|" + d.cexVenue + "|" + d.canonical
		if !demandSet[k] {
			demandSet[k] = true
			demand = append(demand, d)
		}
	}
	paperRows.Close()
	if err := paperRows.Err(); err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("active paper rows: %w", err)
	}

	// D-144②：mt_venue 集=ticks 在流 MT 账户动态枚举（env 钉死单账户删除；
	// 新 broker attach 即自动入簿零人工）。逐账户带 symbol 列——demand 簿对
	// exSym 只认该账户自身采样面（禁借用：XAUUSD 点差账户间实证失真）。
	acctRows, err := st.Pool().Query(ctx, `SELECT venue, venue_type, symbol FROM ticks
		WHERE venue_type IN ('mt4','mt5') AND ts > now() - interval '`+mtStreamWindow+`'
		GROUP BY venue, venue_type, symbol ORDER BY venue, symbol`)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("list mt stream accounts: %w", err)
	}
	acctIdx := map[string]int{}
	var accts []mtAcct
	for acctRows.Next() {
		var v, vt, sym string
		if err := acctRows.Scan(&v, &vt, &sym); err != nil {
			acctRows.Close()
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("scan mt account: %w", err)
		}
		if i, ok := acctIdx[v]; ok {
			accts[i].syms = append(accts[i].syms, sym)
			continue
		}
		acctIdx[v] = len(accts)
		accts = append(accts, mtAcct{venue: v, vtype: vt, syms: []string{sym}})
	}
	acctRows.Close()
	if err := acctRows.Err(); err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("mt account rows: %w", err)
	}

	equiv, err := st.ListAssetEquiv(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("list asset equiv: %w", err)
	}

	caps, err := loadSymbolCapabilities(ctx, st) // 能力账本（裁定注记53③，读面在 capabilities.go）
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	return combos, uni, demand, accts, equiv, caps, nil
}
