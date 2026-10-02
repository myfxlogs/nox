// D-338 S2：XASSET 白名单表加载器（env 类目错置勘正——风险政策数据入表，boot 一次性加载无热加载）。
// environment 过滤下沉 SQL：simulated=true 仅 demo / false 仅 real / NULL 两域通用（exchange_configs 习惯）。
// 加载失败 → 调用方 startup fatal（fail-closed：白名单读不出=不开）；零行 → 合法态（XASSET 全拒=零信任，同 env 空串）。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// XAssetAllowRow xasset_allow_combos 行（调用方映射到 exec.XAssetComboAllowSpec——
// pgstore 不能 import exec：exec→pgstore 既有依赖方向成环；结构同构零信息损失，
// exec.XAssetComboAllowSpec 结构不变约束保持。D-367 S2：NotionalUSD 指针=NULL 语义
// 显式（NULL=回退 ARB_OPP_NOTIONAL_USD 全局缺省），同构零信息损失约束随列扩展保持）。
type XAssetAllowRow struct {
	CanonicalBuy  string
	CanonicalSell string
	VenueBuy      string
	VenueSell     string
	MaxLongQty    float64
	MaxShortQty   float64
	MaxGrossUSD   float64
	NotionalUSD   *float64 // D-367 S2：组合级 sizing 名义额；NULL=回退全局 env
	MarginUSD     *float64 // D-010：组合保证金总预算（双腿占用和上限；与 notional_usd 互斥）
	Note          string
}

func (s *Store) LoadXAssetAllowCombos(ctx context.Context, environment string) ([]XAssetAllowRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT canonical_buy, canonical_sell, venue_key_buy AS venue_buy, venue_key_sell AS venue_sell, max_long_qty, max_short_qty, max_gross_usd, notional_usd, margin_usd, note
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
			&r.MaxLongQty, &r.MaxShortQty, &r.MaxGrossUSD, &r.NotionalUSD, &r.MarginUSD, &r.Note); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// D-512 T9：冷却叠加层（clean 保留 / cooling 拒入 / expired 自动复资格）。
	kept, _, err := s.ApplyComboCooldown(ctx, out, time.Now())
	if err != nil {
		return nil, fmt.Errorf("combo cooldown: %w", err)
	}
	return kept, nil
}

// CarryUniverseRow fin_carry_universe 行（D-336 S2 扫描快照）。
type CarryUniverseRow struct {
	Canonical        string
	MtVenue          string
	CexVenue         string
	NetAnnualPct     float64
	FundingAnnualPct float64
	SwapLongPct      *float64
	SwapShortPct     *float64
	SwapFree         bool
	Structure        string
	Note             string // D-345：suspect 疑因标注（干净组合空串）
}

// ReplaceCarryUniverse D-336 S2：写入当轮全行（当前快照语义——先清当轮前全表，
// 历史靠 ts 保留 30d 由既有清理策略兜底）。事务内单轮原子。
func (s *Store) ReplaceCarryUniverse(ctx context.Context, rows []CarryUniverseRow) error {
	batch := &pgx.Batch{}
	batch.Queue(`DELETE FROM fin_carry_universe`)
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO fin_carry_universe (canonical, mt_venue, cex_venue, net_annual_pct,
				funding_annual_pct, swap_long_pct, swap_short_pct, swap_free, structure, note)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			r.Canonical, r.MtVenue, r.CexVenue, r.NetAnnualPct, r.FundingAnnualPct,
			r.SwapLongPct, r.SwapShortPct, r.SwapFree, r.Structure, r.Note)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range rows {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("insert carry universe: %w", err)
		}
	}
	return nil
}
