// capital_coverage_venue.go —— D-358 S3：CEX/MT 账户余额 per-venue 行（原 loader 第⑤步
// 固定「未采」行翻实值；空表回退占位）。数据结构与渲染在 capital_coverage*.go。
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// uncollectedRow CEX/MT「未采」占位行（D-356 原固定行；D-358 起作快照表空回退，语义不变）。
func uncollectedRow() CapitalCoverageRow {
	return CapitalCoverageRow{
		Pool:   "CEX/MT 账户余额",
		Status: "未采",
		Note:   "采集未立项——余额未入库，本行为决策提示占位",
	}
}

// venueTypeCn D-125 建、D-130 域2②改按 venue 原生中文词（与 dashboard 服务端同名函数同映射；
// digest 独立 module 无法 import internal/，就地镜像——两处文本一致性由双实现各自测试锚维持）。
func venueTypeCn(venue, vt string) string {
	switch venue {
	case "binance":
		switch vt {
		case "perp":
			return "合约"
		case "spot":
			return "现货"
		}
	case "okx":
		switch vt {
		case "funding":
			return "资金账户"
		case "unified":
			return "交易账户"
		case "earn":
			return "赚币"
		}
	case "gate":
		switch vt {
		case "perp":
			return "合约"
		case "earn":
			return "理财"
		case "futures":
			return "交割"
		case "spot":
			return "现货"
		}
	}
	switch vt {
	case "spot":
		return "现货"
	case "perp":
		return "合约"
	case "futures":
		return "交割"
	case "funding":
		return "资金账户"
	case "unified":
		return "交易账户"
	case "earn":
		return "赚币"
	case "mt4":
		return "MT4"
	case "mt5":
		return "MT5"
	default:
		return vt
	}
}

// balanceCcyFoldable D-130 域3：余额币种可折入合计（USDT≈USD 1:1；空=历史行 USD 口径）。
func balanceCcyFoldable(ccy string) bool {
	return ccy == "" || ccy == "USD" || ccy == "USDT"
}

// loadVenueAccountRows ⑤CEX/MT 账户余额（D-358 S3）：fin_venue_account_snapshot 每 venue
// 最新快照一行（DISTINCT ON (venue,venue_type)，D-358fix：同 venue 双 type 独立资金池不折叠）。
// 币种核证结论（migration 0111 头注）：原币混合——
// currency=USDT/USD 行 equity_raw 同视 1:1 入总资金（D-361 S1 业主令）；非 USDT/USD 不折算，
// 余额「—」+Note 标原币值（D-358 §4 边界如实标）。venue 现金=闲置（Productive=false）
// ——有数但不在赚，正是面板要暴露的「睡钱」。D-127：行内附占用/可用注记并聚合占用
// （free_margin_raw 已存=D-405；fm≤0/NULL=未采 fail-closed 不计）。表空/查询失败 → 单行「未采」回退。
func loadVenueAccountRows(ctx context.Context, pool *pgxpool.Pool) ([]CapitalCoverageRow, venueOcc) {
	vaRows, err := pool.Query(ctx, `
		SELECT DISTINCT ON (venue, venue_type, currency) venue, venue_type, equity_raw, free_margin_raw, COALESCE(currency,''), fetched_at
		FROM fin_venue_account_snapshot
		ORDER BY venue, venue_type, currency, fetched_at DESC`)
	if err != nil {
		return []CapitalCoverageRow{uncollectedRow()}, venueOcc{}
	}
	defer vaRows.Close()
	type snapRow struct {
		venue, vt, ccy     string
		equity, freeMargin *float64
		fetched            time.Time
	}
	var snaps []snapRow
	for vaRows.Next() {
		var r snapRow
		if err := vaRows.Scan(&r.venue, &r.vt, &r.equity, &r.freeMargin, &r.ccy, &r.fetched); err != nil {
			return []CapitalCoverageRow{uncollectedRow()}, venueOcc{}
		}
		snaps = append(snaps, r)
	}
	if err := vaRows.Err(); err != nil {
		return []CapitalCoverageRow{uncollectedRow()}, venueOcc{}
	}
	// '' 币残影行并入主币行（同池双计防稀释——敞口闸分母口径修，与 pgstore 同规）。
	for i := range snaps {
		if snaps[i].ccy != "" {
			continue
		}
		named := -1
		for j := range snaps {
			if snaps[j].venue == snaps[i].venue && snaps[j].vt == snaps[i].vt && snaps[j].ccy != "" &&
				(named < 0 || snaps[j].fetched.After(snaps[named].fetched)) {
				named = j
			}
		}
		if named < 0 {
			continue
		}
		if snaps[i].fetched.After(snaps[named].fetched) {
			snaps[named].equity, snaps[named].freeMargin, snaps[named].fetched =
				snaps[i].equity, snaps[i].freeMargin, snaps[i].fetched
		}
		snaps[i].ccy = "\x00" // 标记弃行（不落 coverage 行）
	}
	var out []CapitalCoverageRow
	var occ venueOcc
	for _, s := range snaps {
		if s.ccy == "\x00" {
			continue
		}
		venue, vt, ccy, equity, freeMargin, fetched := s.venue, s.vt, s.ccy, s.equity, s.freeMargin, s.fetched
		// D-130 域2①：binance 资金钱包面官方已废除并入现货——行不再枚举。
		// （earn 面回归：分币快照口径解除 D-133「只统计 USDT」约束，服务端同规。）
		if venue == "binance" && vt == "funding" {
			continue
		}
		// D-125：池标签去「venue账户」前缀；D-130 域2②面名随 venue 原生词。
		row := CapitalCoverageRow{Pool: venue + "·" + venueTypeCn(venue, vt)}
		age := time.Since(fetched)
		ageNote := fmt.Sprintf("%s 快照 %dh 前", venueTypeCn(venue, vt), int(age.Hours()))
		if age > 24*time.Hour {
			ageNote += "（陈旧）"
		}
		switch {
		case equity != nil && (ccy == "USDT" || ccy == "USD"): // D-361 S1
			row.BalanceUSD = equity
			row.BalanceCcy = ccy
			row.RealMoney = true
			// D-127：占用聚合+可用注记（fm≤0/NULL=未采 fail-closed；eq<fm 数据异常不猜）。
			// D-128：状态=业务词（闲置/部分占用/占用未知），「有数」黑话清除。
			switch {
			case freeMargin != nil && *freeMargin > 0 && *equity >= *freeMargin:
				o := *equity - *freeMargin
				occ.sum += o
				occ.known++
				row.Note = ageNote + fmt.Sprintf("，可用 %.2f", *freeMargin)
				if o > 0 {
					row.Status = "部分占用"
					occ.items = append(occ.items, fmt.Sprintf("%s $%.2f", row.Pool, o))
				} else {
					row.Status = "闲置"
				}
			default:
				occ.missing++
				row.Note = ageNote + "，可用未采"
				row.Status = "占用未知"
			}
		case equity != nil:
			// D-130 域3：原币直显（合计门 balanceCcyFoldable 拒纳）
			row.BalanceUSD = equity
			row.BalanceCcy = ccy
			row.Status = "未折算"
			row.Note = fmt.Sprintf("%s，净值 %.2f %s（非 USDT/USD 不折算）", ageNote, *equity, ccy)
		default:
			row.Status = "未采"
			row.Note = ageNote + "，净值空"
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		return []CapitalCoverageRow{uncollectedRow()}, venueOcc{}
	}
	return out, occ
}
