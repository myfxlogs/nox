// XASSET 白名单表加载器（env 类目错置勘正——风险政策数据入表，boot 一次性加载无热加载）。
// environment 过滤下沉 SQL：simulated=true 仅 demo / false 仅 real / NULL 两域通用（exchange_configs 习惯）。
// 加载失败 → 调用方 startup fatal（fail-closed：白名单读不出=不开）；零行 → 合法态（XASSET 全拒=零信任，同 env 空串）。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"nox/internal/econkb"

	"github.com/jackc/pgx/v5"
)

// XAssetAllowRow xasset_allow_combos 行（调用方映射到 exec.XAssetComboAllowSpec——
// pgstore 不能 import exec：exec→pgstore 既有依赖方向成环；结构同构零信息损失，
// exec.XAssetComboAllowSpec 结构不变约束保持。D-367 S2：NotionalUSD 指针=NULL 语义
// 显式（NULL=回退 NOX_OPP_NOTIONAL_USD 全局缺省），同构零信息损失约束随列扩展保持）。
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
	// D-581：sizing 模式——fixed=腿帽固化（现值语义）；pct=funding_pct 原样，
	// 腿帽 NULL→armed 径活池折算；system=同 pct 但比率源=app_config
	// grad_system_alloc_pct（决策席域可调，业主零 sizing 必填）。
	SizingMode string
	FundingPct *float64
	Note       string
	Tier       string // D-625 P0：阶级层（armed|trial）
}

func (s *Store) LoadXAssetAllowCombos(ctx context.Context, environment string) ([]XAssetAllowRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT canonical_buy, canonical_sell, venue_key_buy AS venue_buy, venue_key_sell AS venue_sell,
		       COALESCE(max_long_qty,0), COALESCE(max_short_qty,0), COALESCE(max_gross_usd,0),
		       notional_usd, margin_usd, COALESCE(sizing_mode,'fixed'), funding_pct, note, tier
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
			&r.MaxLongQty, &r.MaxShortQty, &r.MaxGrossUSD, &r.NotionalUSD, &r.MarginUSD,
			&r.SizingMode, &r.FundingPct, &r.Note, &r.Tier); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 冷却叠加层（clean 保留 / cooling 拒入 / expired 自动复资格）。
	kept, _, err := s.ApplyComboCooldown(ctx, out, time.Now())
	if err != nil {
		return nil, fmt.Errorf("combo cooldown: %w", err)
	}
	// tier 消费闸（D-625 P0 续件③）：trial 仓名义额超 trial_tier_notional_usd
	// →拒入本拍（fail-closed——闸是消费级拒单非删行；armed 级不消费=行为不变）。
	if cap, ok, cerr := s.AppConfigFloat(ctx, "trial_tier_notional_usd"); cerr == nil && ok {
		var final []XAssetAllowRow
		for _, c := range kept {
			if c.Tier == "trial" && c.NotionalUSD != nil && *c.NotionalUSD > cap {
				_ = s.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
					"[tier 闸] trial 组合 %s→%s|%s×%s 名义额 %.0f 超 trial_tier_notional_usd %.0f——拒入本拍（授权另闸 D-624 不动）",
					c.CanonicalBuy, c.CanonicalSell, c.VenueBuy, c.VenueSell, *c.NotionalUSD, cap), 6*time.Hour)
				continue
			}
			final = append(final, c)
		}
		kept = final
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

// ListCfdCarryNetNow D-060①：纸面卡 CFD 行「当前」净 carry 数据源——
// fin_carry_universe 现快照按 mt_venue|cex_venue|canonical|structure 键投影
// net_annual_pct（已含 swap 修正；suspect 行不剔——展示面忠实透库值）。
func (s *Store) ListCfdCarryNetNow(ctx context.Context) (map[string]float64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT mt_venue, cex_venue, canonical, structure, net_annual_pct
		FROM fin_carry_universe
		WHERE structure IN ('long_cfd_short_perp','short_cfd_long_perp')`)
	if err != nil {
		return nil, fmt.Errorf("list cfd carry net now: %w", err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var mt, cex, canon, structure string
		var net float64
		if err := rows.Scan(&mt, &cex, &canon, &structure, &net); err != nil {
			return nil, err
		}
		out[mt+"|"+cex+"|"+canon+"|"+structure] = net
	}
	return out, rows.Err()
}

// RetireCombosMissingVenue D-584 venue 退役守卫（armed 评估环每拍调）：
// venue 从 broker_accounts（MT 系）+ exchange_configs（CEX 系）双簿同时
// 消失的组合自动 enabled=false + note 留痕——零凭据组合不得挂白名单。
// 返退役行数（留痕叙事用）。
func (s *Store) RetireCombosMissingVenue(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE xasset_allow_combos SET enabled=false,
		       note = note || ' [venue退役：' || now()::date || ']'
		WHERE enabled
		  AND (venue_buy NOT IN (SELECT venue FROM broker_accounts)
		       AND venue_buy NOT IN (SELECT venue FROM exchange_configs)
		    OR venue_sell NOT IN (SELECT venue FROM broker_accounts)
		       AND venue_sell NOT IN (SELECT venue FROM exchange_configs))`)
	if err != nil {
		return 0, fmt.Errorf("retire combos missing venue: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DisablePitHitCombos D-587④：armed 评估环坑闸——enabled 组合 canonical/venue
// 命中坑签名且 pit_exempt_d 未落 D# 豁免 → 自动 enabled=false+note 留痕。
func (s *Store) DisablePitHitCombos(ctx context.Context, dir string) (int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, canonical_buy, canonical_sell, venue_buy, venue_sell, pit_exempt_d
		FROM xasset_allow_combos WHERE enabled=true`)
	if err != nil {
		return 0, fmt.Errorf("list armed combos pit gate: %w", err)
	}
	type cand struct {
		id             int64
		cb, cs, vb, vs string
		ex             *string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.cb, &c.cs, &c.vb, &c.vs, &c.ex); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan armed combo: %w", err)
		}
		cands = append(cands, c)
	}
	rows.Close()
	disabled := 0
	for _, c := range cands {
		ex := ""
		if c.ex != nil {
			ex = *c.ex
		}
		blocked, hits := econkb.PitBlocked(dir, econkb.Input{
			Canonical: c.cb, Canonicals: []string{c.cb, c.cs}, VenueA: c.vb, VenueB: c.vs,
		}, ex)
		if !blocked {
			continue
		}
		ids := make([]string, 0, len(hits))
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		if _, err := s.pool.Exec(ctx, `
			UPDATE xasset_allow_combos SET enabled=false,
			note = note || '｜' || $1 WHERE id=$2`,
			fmt.Sprintf("坑闸摘：命中 %v（armed 自动摘——豁免须 pit_exempt_d 落 D#）", ids), c.id); err != nil {
			return disabled, fmt.Errorf("disable pit combo %d: %w", c.id, err)
		}
		disabled++
	}
	return disabled, nil
}

// DisabledCombosOpenPositions D-599：armed 被摘/被坑组合四元组命中的 OPEN
// 持仓 id 集（enabled=false 组合仍有在仓的仓=裸跑病灶，退出触发源）。
func (s *Store) DisabledCombosOpenPositions(ctx context.Context) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT p.id FROM positions p
		JOIN xasset_allow_combos c ON c.enabled=false
		WHERE p.status IN ('OPEN','LEGGED')
		  -- D-599 返修：双腿配对谓词（非腿级 OR——共腿不误杀：须买腿∧卖腿双腿同中）
		  AND EXISTS (SELECT 1 FROM position_legs lb WHERE lb.position_id=p.id
		              AND lb.venue=c.venue_buy AND lb.symbol=c.canonical_buy)
		  AND EXISTS (SELECT 1 FROM position_legs ls WHERE ls.position_id=p.id
		              AND ls.venue=c.venue_sell AND ls.symbol=c.canonical_sell)`)
	if err != nil {
		return nil, fmt.Errorf("disabled combos open positions: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan pos id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
