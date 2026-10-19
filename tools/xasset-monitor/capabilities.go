// capabilities.go —— 账户能力账本读面（裁定注记53③/簿饥饿销账遗留②）：
// broker_symbol_map=symbolPlan 策展+服务器实存校验的账户能力面。
// 组合×venue 展开按此过滤——无映射 canonical 不产对（TRXUSDT@110660304 类
// 结构性 skip 噪音根除）。键=裸户号（map.venue 域）；纯读零写（禁线：不动
// 映射表本体）。
package main

import (
	"context"
	"fmt"

	"nox/internal/store/pgstore"
)

// loadSymbolCapabilities 全账户能力集：裸户号→canonical 集。
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
		caps[v][c] = true
	}
	if err := capRows.Err(); err != nil {
		return nil, fmt.Errorf("symbol capability rows: %w", err)
	}
	return caps, nil
}
