// D-338 S2：XASSET 白名单表加载器（env 类目错置勘正——风险政策数据入表，boot 一次性加载无热加载）。
// environment 过滤下沉 SQL：simulated=true 仅 demo / false 仅 real / NULL 两域通用（exchange_configs 习惯）。
// 加载失败 → 调用方 startup fatal（fail-closed：白名单读不出=不开）；零行 → 合法态（XASSET 全拒=零信任，同 env 空串）。
package pgstore

import (
	"context"
	"fmt"
)

// XAssetAllowRow xasset_allow_combos 行（调用方映射到 exec.XAssetComboAllowSpec——
// pgstore 不能 import exec：exec→pgstore 既有依赖方向成环；结构同构零信息损失，
// exec.XAssetComboAllowSpec 结构不变约束保持）。
type XAssetAllowRow struct {
	CanonicalBuy  string
	CanonicalSell string
	VenueBuy      string
	VenueSell     string
	MaxLongQty    float64
	MaxShortQty   float64
	MaxGrossUSD   float64
	Note          string
}

func (s *Store) LoadXAssetAllowCombos(ctx context.Context, environment string) ([]XAssetAllowRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT canonical_buy, canonical_sell, venue_buy, venue_sell, max_long_qty, max_short_qty, max_gross_usd, note
		FROM xasset_allow_combos
		WHERE enabled AND (simulated IS NULL OR simulated = ($1='demo'))`, environment)
	if err != nil {
		return nil, fmt.Errorf("load xasset allow combos: %w", err)
	}
	defer rows.Close()
	var out []XAssetAllowRow
	for rows.Next() {
		var r XAssetAllowRow
		if err := rows.Scan(&r.CanonicalBuy, &r.CanonicalSell, &r.VenueBuy, &r.VenueSell,
			&r.MaxLongQty, &r.MaxShortQty, &r.MaxGrossUSD, &r.Note); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
