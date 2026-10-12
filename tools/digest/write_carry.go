// write_carry.go —— D-341 S4/D-350/D-366 S1：spot-carry 候选/纸面 + 带内循环真实费率轨
// 六函数+三类型整组自 write.go 拆出（D-366 超线拆分：同包纯搬移零行为变更）。
// 段渲染函数由 main.run 拼接；loader 直连 PG（digest 独立 module 复用既有连接惯例）。
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// resolvedEvent/parsePendingHistory/appendResolvedArchive 已拆至 pending.go（D-309 纯移动）。

// —— D-341 S4：带内循环·真实费率轨段（独立函数追加，D-336 段同规互不触碰）——

// CycleRealrateRow v_cycle_realrate 视图行（per-cycle 对照三列）。
type CycleRealrateRow struct {
	PositionID          int64    `json:"position_id"`
	OpportunityID       string   `json:"opportunity_id"`
	Status              string   `json:"status"`
	ExitKind            *string  `json:"exit_kind"`
	RealizedPnl         *float64 `json:"realized_pnl"`
	EstimatedCarryUSD   float64  `json:"estimated_carry_usd"`
	SimulatedCarryUSD   float64  `json:"simulated_carry_usd"`
	CycleConvergenceUSD float64  `json:"cycle_convergence_usd"`
}

// RenderCycleRealrateSection D-341 S4：「带内循环·真实费率轨」段（estimated=real facts 账 /
// simulated=demo sim 费率账 / convergence=收敛盈亏；独立函数，调用方 main.run 拼接）。
func RenderCycleRealrateSection(rows []CycleRealrateRow) string {
	var buf strings.Builder
	buf.WriteString("\n## 带内循环·真实费率轨（D-341 S4）\n\n")
	buf.WriteString("> estimated=real facts 账（swapledger）/ simulated=demo sim 费率账（income_events）/ convergence=收敛盈亏（realized−simulated）。\n\n")
	if len(rows) == 0 {
		buf.WriteString("（暂无 cycle 行）\n")
		return buf.String()
	}
	buf.WriteString("| pos | 状态 | exit_kind | realized$ | estimated carry$ | simulated carry$ | convergence$ |\n")
	buf.WriteString("|------|------|-----------|-----------|------------------|------------------|---------------|\n")
	for _, r := range rows {
		exit := "—"
		if r.ExitKind != nil {
			exit = *r.ExitKind
		}
		realized := "—"
		if r.RealizedPnl != nil {
			realized = fmt.Sprintf("%.2f", *r.RealizedPnl)
		}
		buf.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %.2f | %.2f | %.2f |\n",
			r.PositionID, r.Status, exit, realized, r.EstimatedCarryUSD, r.SimulatedCarryUSD, r.CycleConvergenceUSD))
	}
	return buf.String()
}

// loadCycleRealrate 查 v_cycle_realrate 视图最近 10 行（D-341 S4 段数据源）。
func loadCycleRealrate(ctx context.Context, pool *pgxpool.Pool) ([]CycleRealrateRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT position_id, opportunity_id, status, exit_kind, realized_pnl,
		       estimated_carry_usd, simulated_carry_usd, cycle_convergence_usd
		FROM v_cycle_realrate ORDER BY position_id DESC LIMIT 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CycleRealrateRow
	for rows.Next() {
		var r CycleRealrateRow
		if err := rows.Scan(&r.PositionID, &r.OpportunityID, &r.Status, &r.ExitKind,
			&r.RealizedPnl, &r.EstimatedCarryUSD, &r.SimulatedCarryUSD, &r.CycleConvergenceUSD); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SpotCarryUniverseRow fin_carry_universe spot_carry 行（D-350 ①段数据源）。
type SpotCarryUniverseRow struct {
	Symbol, Venue      string
	FundingAnn, NetAnn float64
}

// PaperSpotCarryRow paper_spot_carry 行（D-350 ②段数据源）。
// D-022：venue 列自 0160（D-555 S1）起拆 venue_a/venue_b（venue_key_a/b 生成列），
// digest 未同步=查询列名遗漏（git log -S 定性：0160 列改名，非逻辑变更）。
type PaperSpotCarryRow struct {
	ID                                                  int64
	VenueA, VenueB, Symbol, Side, Status                string
	EntryFundingAnn, QtyUSD, LastFundingAnn, AccruedUSD float64
	EntryTs                                             string
	ExitReason                                          string
}

// RenderSpotCarrySection D-350 ①：「spot-carry 候选」段（band 20-300，前三行；
// 独立结构独立排名，不与 CFD 混排；D-353 S2 起 band 滤在 loader SQL）。
func RenderSpotCarrySection(rows []SpotCarryUniverseRow) string {
	if len(rows) == 0 {
		return ""
	}
	var buf strings.Builder
	buf.WriteString("\n## spot-carry 候选（band 20-300 %/yr，独立排名）\n\n")
	buf.WriteString("| symbol | venue | funding %/yr | net %/yr |\n")
	buf.WriteString("|---|---|---|---|\n")
	for i, r := range rows {
		if i >= 3 {
			break
		}
		buf.WriteString(fmt.Sprintf("| %s | %s | %.2f | %.2f |\n", r.Symbol, r.Venue, r.FundingAnn, r.NetAnn))
	}
	return buf.String()
}

// RenderPaperSpotCarrySection D-350 ②：「spot-carry 纸面」段（活跃行+累计 accrued）。
func RenderPaperSpotCarrySection(rows []PaperSpotCarryRow) string {
	if len(rows) == 0 {
		return ""
	}
	var buf strings.Builder
	buf.WriteString("\n## spot-carry 纸面（雷达快照费率累积）\n\n")
	buf.WriteString("| id | venues | symbol | side | qty$ | entry %/yr | last %/yr | accrued$ | entry |\n")
	buf.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		buf.WriteString(fmt.Sprintf("| %d | %s×%s | %s | %s | %.0f | %.2f | %.2f | %.4f | %s |\n",
			r.ID, r.VenueA, r.VenueB, r.Symbol, r.Side, r.QtyUSD, r.EntryFundingAnn, r.LastFundingAnn, r.AccruedUSD, r.EntryTs))
	}
	return buf.String()
}

// loadSpotCarryUniverse 查 spot_carry 候选（net 倒序前三；D-350 ①段数据源）。
// D-353 S2：band[20,300] 与纸面闸同值对齐——FRONG 类 band 外极端行不再霸榜
// （fin_carry_universe 表保持诚实全量，band 只作用于 digest 展示层）。
func loadSpotCarryUniverse(ctx context.Context, pool *pgxpool.Pool) ([]SpotCarryUniverseRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT cex_venue, canonical, funding_annual_pct, net_annual_pct
		FROM fin_carry_universe WHERE structure = 'spot_carry'
		  AND net_annual_pct BETWEEN 20 AND 300
		ORDER BY net_annual_pct DESC LIMIT 3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpotCarryUniverseRow
	for rows.Next() {
		var r SpotCarryUniverseRow
		if err := rows.Scan(&r.Venue, &r.Symbol, &r.FundingAnn, &r.NetAnn); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// loadPaperSpotCarry 查活跃纸面行（D-350 ②段数据源）。
func loadPaperSpotCarry(ctx context.Context, pool *pgxpool.Pool) ([]PaperSpotCarryRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, venue_a, venue_b, symbol, side, status, entry_funding_ann, qty_usd,
		       COALESCE(last_funding_ann, 0), accrued_usd,
		       to_char(entry_ts, 'MM-DD HH24:MI'), exit_reason
		FROM paper_spot_carry WHERE status IN ('active', 'demo') ORDER BY accrued_usd DESC LIMIT 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaperSpotCarryRow
	for rows.Next() {
		var r PaperSpotCarryRow
		if err := rows.Scan(&r.ID, &r.VenueA, &r.VenueB, &r.Symbol, &r.Side, &r.Status, &r.EntryFundingAnn,
			&r.QtyUSD, &r.LastFundingAnn, &r.AccruedUSD, &r.EntryTs, &r.ExitReason); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
