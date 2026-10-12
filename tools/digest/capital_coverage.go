// capital_coverage.go —— D-356 资金不眠覆盖率段（快照口径，五源逐池一行+汇总）。
// 口径 SSOT: docs/D-356设计与施工提示词-资金不眠覆盖率面板-2026-09-20-Devin.md §1.1。
// 各源查询失败/空表 → 该行省略不报错；四数据源全缺 → 渲染「数据源全缺」诚实空态。
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CapitalCoverageRow 覆盖率段一行（渲染纯函数输入）。
type CapitalCoverageRow struct {
	Pool       string   // 资金池名
	BalanceUSD *float64 // nil=无 USD 价/未采 → 余额列「—」，不入汇总
	Status     string
	APRPct     *float64 // nil=无年化口径 → 年化列按 APRNilText 或「—」
	APRNilText string   // APRPct=nil 时年化列文本（如「新开仓」；空=「—」）
	Note       string
	Productive bool   // 计入在产分子（余额非 nil 才实际计入）
	RealMoney  bool   // 入总资金（纸面非真金=false；D-127 占用行=false=净值子集视图防双重计入）
	BalanceCcy string // D-130 域3：余额原币币种（USDT/USD/…；空=USD 口径）
}

// loadCapitalCoverage D-356 S1：五源逐池查询（纯读），返回行序
// TRX→CMC→保证金占用→CEX/MT 未采固定行（capital_snapshots 腿随 D-120 退役）。
// D-127：占用行=Σ(equity−free_margin) 账户级子集视图，名义额退出合计降 note 参考值。
// 数据源全缺 → 空切片（渲染「数据源全缺」）。
func loadCapitalCoverage(ctx context.Context, pool *pgxpool.Pool) ([]CapitalCoverageRow, error) {
	rows := []CapitalCoverageRow{}
	if r, ok := loadTRXPoolRow(ctx, pool); ok {
		rows = append(rows, r)
	}
	if r, ok := loadCMCPoolRow(ctx, pool); ok {
		rows = append(rows, r)
	}
	venueRows, occ := loadVenueAccountRows(ctx, pool)
	rows = append(rows, loadMarginOccupancyRow(ctx, pool, occ))
	rows = append(rows, venueRows...)
	return rows, nil
}

// loadTRXPoolRow ①TRX 池：余额=fin_trx_account 最新 (balance_sun+tron_power)/1e6×TRX USD 价；
// 年化=fin_trx_vote_apr 最新 current_rate + 能量租金 APR（D-258 30d 口径）。
func loadTRXPoolRow(ctx context.Context, pool *pgxpool.Pool) (CapitalCoverageRow, bool) {
	var balanceSun, tronPower, energyDelegated int64
	if err := pool.QueryRow(ctx, `
		SELECT balance_sun, tron_power, energy_delegated FROM fin_trx_account
		ORDER BY fetched_at DESC LIMIT 1`).Scan(&balanceSun, &tronPower, &energyDelegated); err != nil {
		return CapitalCoverageRow{}, false
	}
	row := CapitalCoverageRow{Pool: "TRX 质押+投票+能量", BalanceCcy: "USD", Status: "在赚",
		Productive: true, RealMoney: true}
	// 年化成分 1：投票
	voteAPR, hasVote := 0.0, false
	if err := pool.QueryRow(ctx, `
		SELECT current_rate FROM fin_trx_vote_apr
		ORDER BY fetched_at DESC LIMIT 1`).Scan(&voteAPR); err == nil {
		hasVote = true
	}
	// 年化成分 2：能量租金（D-258 30d 口径；样本不足/零在押/无样本 → 不猜不记）
	energyAPR, energyOK, energyWhy := trxEnergyAPRPct(ctx, pool, energyDelegated, time.Now().UTC(), 30)
	aprNote := ""
	if hasVote {
		aprNote = fmt.Sprintf("投票 %.2f%%", voteAPR)
		row.APRPct = &voteAPR
	}
	if energyOK {
		aprNote += fmt.Sprintf("+能量租赁 %.2f%%", energyAPR)
		if row.APRPct != nil {
			v := *row.APRPct + energyAPR
			row.APRPct = &v
		} else {
			row.APRPct = &energyAPR
		}
	} else if hasVote {
		aprNote += "（能量" + energyWhy + "未计）"
	}
	// TRX USD 价三级 fallback：fin_cmc_prices(X-AFQUL=TRX) → ticks TRXUSDT 中间价 → 无价
	// 余额=balance+tron_power（R1：在押投票权与流动余额同池计，构成入说明列）
	balTRX := float64(balanceSun+tronPower) / 1e6
	if price, ok := trxUSDPrice(ctx, pool); ok {
		b := balTRX * price
		row.BalanceUSD = &b
		row.Note = fmt.Sprintf("%s；余额=可用 %.0f+投票权 %.0f TRX", aprNote,
			float64(balanceSun)/1e6, float64(tronPower)/1e6)
	} else {
		row.Note = fmt.Sprintf("%s；TRX 数量 %.0f（可用 %.0f+投票权 %.0f，无 USD 价，不猜）",
			aprNote, balTRX, float64(balanceSun)/1e6, float64(tronPower)/1e6)
	}
	return row, true
}

// trxEnergyAPRPct D-258 口径（同 pgstore TrxEnergyRentalAPR 就地复刻——digest 独立 module
// 无法 import 主模块 internal/）：年化=(区间租金 TRX/样本天数×365)/在押 TRX×100。
// 无样本/样本<days（冷启动）/零在押/查询失败 → (0,false,原因)，不猜。
func trxEnergyAPRPct(ctx context.Context, pool *pgxpool.Pool, energyDelegated int64, now time.Time, days int) (float64, bool, string) {
	from := now.AddDate(0, 0, -days)
	var rentalSun int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_sun),0) FROM fin_trx_txs
		WHERE kind='rental_income' AND ts >= $1 AND ts < $2`, from, now).Scan(&rentalSun); err != nil {
		return 0, false, "查询失败"
	}
	var firstTs time.Time
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(MIN(ts),'0001-01-01T00:00:00Z'::timestamptz) FROM fin_trx_txs
		WHERE kind='rental_income' AND ts >= $1 AND ts < $2`, from, now).Scan(&firstTs); err != nil {
		return 0, false, "查询失败"
	}
	if firstTs.IsZero() {
		return 0, false, "无样本，"
	}
	sampleDays := int(now.Sub(firstTs).Hours()/24) + 1
	if sampleDays < days {
		return 0, false, fmt.Sprintf("样本%d天<%d天，", sampleDays, days)
	}
	energyTRX := float64(energyDelegated) / 1e6
	if energyTRX == 0 {
		return 0, false, "零在押，"
	}
	return energyAPRFromSample(float64(rentalSun)/1e6, sampleDays, energyTRX), true, ""
}

// energyAPRFromSample D-258 公式纯函数：(区间租金 TRX/样本天数×365)/在押 TRX×100。
func energyAPRFromSample(rentalTRX float64, sampleDays int, energyTRX float64) float64 {
	return (rentalTRX / float64(sampleDays) * 365) / energyTRX * 100
}

// trxUSDPrice TRX USD 价：fin_cmc_prices 最新 mid（instrument X-AFQUL=TRX，D-278 同款）
// → ticks TRXUSDT 最新 bid/ask 中间价 → 无价 false。
func trxUSDPrice(ctx context.Context, pool *pgxpool.Pool) (float64, bool) {
	var p float64
	if err := pool.QueryRow(ctx, `
		SELECT mid_price FROM fin_cmc_prices
		WHERE instrument_id='X-AFQUL' AND mid_price > 0
		ORDER BY fetched_at DESC LIMIT 1`).Scan(&p); err == nil && p > 0 {
		return p, true
	}
	if err := pool.QueryRow(ctx, `
		SELECT (bid+ask)/2 FROM ticks
		WHERE symbol='TRXUSDT' AND bid > 0 AND ask > 0
		ORDER BY ts DESC LIMIT 1`).Scan(&p); err == nil && p > 0 {
		return p, true
	}
	return 0, false
}

// loadCMCPoolRow ②CMC 池：余额=fin_cmc_account_summary 最新 total_account_value；
// 状态=fin_cmc_positions 最新快照有行→「手动持仓中（浮盈 $X）」/无行→「现金闲置」。
// 手动域无 carry 口径 → 年化「—」（不入加权年化，§1.1 表）。
func loadCMCPoolRow(ctx context.Context, pool *pgxpool.Pool) (CapitalCoverageRow, bool) {
	var total *float64
	if err := pool.QueryRow(ctx, `
		SELECT total_account_value FROM fin_cmc_account_summary
		ORDER BY fetched_at DESC LIMIT 1`).Scan(&total); err != nil || total == nil {
		return CapitalCoverageRow{}, false
	}
	row := CapitalCoverageRow{Pool: "CMC 账户", BalanceCcy: "USD", Note: "手动持仓监控域",
		BalanceUSD: total, RealMoney: true}
	var pnl *float64
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT SUM(unrealised_pnl), COUNT(*) FROM fin_cmc_positions
		WHERE fetched_at = (SELECT MAX(fetched_at) FROM fin_cmc_positions)`).Scan(&pnl, &n); err == nil && n > 0 {
		row.Status = fmt.Sprintf("$%.2f", *pnl) // D-128：浮盈直数（负号如实）
		row.Note += "；手动持仓中"                    // D-129：说明收说明列
		row.Productive = true
	} else {
		row.Status = "现金闲置"
	}
	return row, true
}

// venueOcc D-127：账户占用聚合（与 dashboard 服务端 venueOcc 同构——digest 独立 module 就地镜像）。
type venueOcc struct {
	sum     float64
	known   int
	missing int
	items   []string // D-129：占用>0 账户明细（收说明列）
}

// loadMarginOccupancyRow ③保证金占用（D-127 新口径，替代原「系统持仓」名义额行）：
// 值=Σ账户占用；名义额退出合计、降格 note 参考值；RealMoney=false=venue 净值子集视图
// （净值行已入分母，占用行不重复计）∧Productive=true 入在产分子；
// 年化仍按名义额折算（息差随名义产生），加权按占用额。
func loadMarginOccupancyRow(ctx context.Context, pool *pgxpool.Pool, occ venueOcc) CapitalCoverageRow {
	row := CapitalCoverageRow{Pool: "保证金占用", BalanceCcy: "USD", Productive: true,
		RealMoney: false}
	var total float64
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(notional_usd), 0), COUNT(*)
		FROM positions
		WHERE status IN ('OPEN', 'OPENING', 'LEGGED', 'CLOSING')
		  AND COALESCE(environment, 'legacy_demo') = 'live'`).Scan(&total, &n); err != nil {
		total = 0 // 查询败=名义参考缺，占用聚合不受影响（venue 行已算）
	}
	// 近7日日均 carry（position_daily_income=系统组合日收益账，全组合计口径）
	var avg *float64
	_ = pool.QueryRow(ctx, `
		SELECT AVG((COALESCE(energy_trx,0)+COALESCE(voting_trx,0))*COALESCE(price,0)+COALESCE(interest_usd,0))
		FROM position_daily_income
		WHERE day >= CURRENT_DATE - INTERVAL '7 days'`).Scan(&avg)

	if occ.known > 0 {
		b := occ.sum
		row.BalanceUSD = &b
		row.Status = "占用中"
	} else if total > 0 {
		row.Status = "占用未知" // 有仓但可用额全未采——fail-closed 不入分子（防虚胖）
	} else {
		row.Status = "零占用"
	}
	note := "账户级占用（含同账户手动仓，腿级切分不可得）"
	if len(occ.items) > 0 { // D-129：占用明细收说明列
		note += "；占用明细 " + strings.Join(occ.items, " · ")
	}
	if occ.missing > 0 {
		note += fmt.Sprintf("；%d 账户可用额未采不计", occ.missing)
	}
	if total > 0 {
		note += fmt.Sprintf("；名义额参考 $%.2f（不入合计）", total)
		if avg != nil {
			a := *avg * 365 / total * 100
			row.APRPct = &a
		} else {
			row.APRNilText = "新开仓"
		}
	} else if occ.known == 0 {
		note = "无活跃仓·无账户占用"
	}
	row.Note = note
	return row
}
