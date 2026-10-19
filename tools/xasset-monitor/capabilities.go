// capabilities.go —— 账户能力账本读面（裁定注记53③/簿饥饿销账遗留②）：
// broker_symbol_map=symbolPlan 策展+服务器实存校验的账户能力面。
// 组合×venue 展开按此过滤——无映射 canonical 不产对（TRXUSDT@110660304 类
// 结构性 skip 噪音根除）。键=裸户号（map.venue 域）；纯读零写（禁线：不动
// 映射表本体）。
package main

import (
	"context"
	"fmt"
	"strings"

	"nox/internal/store/pgstore"
)

// bareCanon MT canonical 裸形归一（USDT/USD 尾裁——与 1055_s2canon_norm 组合行
// MT 腿裸形同款、与 pairs.go 派生 bareOf 同语义单源）：map.canonical 全形
// （TRXUSDT）×组合行裸形（TRX）两侧键域在此合流。
func bareCanon(s string) string {
	if b := strings.TrimSuffix(s, "USDT"); b != s && b != "" {
		return b
	}
	if b := strings.TrimSuffix(s, "USD"); b != s && b != "" {
		return b
	}
	return s
}

// loadSymbolCapabilities 全账户能力集：裸户号→bareCanon(canonical) 集
// （键域=裸形——查侧组合行 1055 后为裸形，双侧同域防错域假滤）。
func loadSymbolCapabilities(ctx context.Context, st *pgstore.Store) (map[string]map[string]bool, error) {
	capRows, err := st.Pool().Query(ctx, `SELECT venue, canonical FROM broker_symbol_map WHERE canonical <> ''`)
	if err != nil {
		return nil, fmt.Errorf("list symbol capabilities: %w", err)
	}
	defer capRows.Close()
	caps := map[string]map[string]bool{}
	for capRows.Next() {
		var v, c string
		if err := capRows.Scan(&v, &c); err != nil {
			return nil, fmt.Errorf("scan symbol capability: %w", err)
		}
		if caps[v] == nil {
			caps[v] = map[string]bool{}
		}
		caps[v][bareCanon(c)] = true
	}
	if err := capRows.Err(); err != nil {
		return nil, fmt.Errorf("symbol capability rows: %w", err)
	}
	return caps, nil
}
