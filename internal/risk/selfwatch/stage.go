// stage.go —— D-033②：Stage 闸就绪条件表（每项=标签+判定+实测数据串）。
// 口径来源：Stage 1=decisions D-023/D-025/D-030（paper ≥7d 净额非负·零事故·
// 子系统在线·cap/白名单）；Stage 2=D-030 账本 30d 年化 ≥8% + D-026 maker 分级
// （≥20 笔 maker 开仓零事故）。
// 组合证据=白名单行精确配对键（venue_a×venue_b×canonical×structure，
// 同 venue 对多品种不串账）；全表文案面向运营——零票号/内部实现词。
package selfwatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"nox/internal/data/fact"
	"nox/internal/data/macro"
	"nox/internal/research/scan"
	"nox/internal/store/pgstore"
)

// fundingFactMaxAge D-027 在线判据——funding facts 流供体最大龄（mark-price WS
// 持续推送，超龄=流死=滚动均值无源）。
const fundingFactMaxAge = 4 * time.Hour

// makerStageOpens D-026 真金分级门槛：≥20 笔 maker 开仓零事故 → Stage 2 可带 maker。
const makerStageOpens = 20

// ①②缺省值（config 全链热读缺省回落）：
// stage1MinRowHoursDefault 目标组合纸面行存活下限（小时）；
// stage1OpenWarmupDefault MT 周开盘后开仓暖场（小时）。
const (
	stage1MinRowHoursDefault = 48
	stage1OpenWarmupDefault  = 2
)

// cfgIntDef app_config 热读 int（缺行/读败→def；显示面 fail-open 用缺省值续判）。
func cfgIntDef(ctx context.Context, d Deps, key string, def int) int {
	if v, ok, err := d.Store.AppConfigInt64(ctx, key); err == nil && ok && v > 0 {
		return int(v)
	}
	return def
}

// cfgStrDef app_config 热读 string（缺行/读败→def）。
func cfgStrDef(ctx context.Context, d Deps, key, def string) string {
	if v, ok, err := d.Store.AppConfigString(ctx, key); err == nil && ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

func stage1Checks(ctx context.Context, d Deps, since time.Time, windowOK bool, ev *scan.PaperWindowEval, now time.Time) []StageCheck {
	rows := []StageCheck{}
	if !windowOK || ev == nil {
		return []StageCheck{
			{Label: "纸面观测窗已配置", OK: false, Data: "窗起点未配置（NOX_PAPER_WINDOW_START）"},
		}
	}
	// 日期制判据（窗 ≥7 天/组合在仓 ≥5d）撤销——Stage 1 改条件制，
	// 见「目标组合纸面行存活」行（since 仍承载窗内净额/事故计数域）。
	_ = since
	// 组合域=白名单实盘启用行的精确配对键（venue_a×venue_b×canonical×
	// structure）——canonical 按 NormalizeJoinKey 同口径对齐行 symbol（白名单
	// XAUUSD ↔ 纸面行 XAU）；canonical/structure 缺 → 该行按「无品种」fail-closed
	// 标出且全表不判就绪。净额按匹配组合求和（重 tuple 去重防双计）。
	pairs, perr := d.Store.XAssetAllowRealPairs(ctx)
	var malformed []string
	comboOf := func(pr pgstore.XAssetRealCombo) *scan.PaperComboStat {
		want := scan.NormalizeJoinKey(pr.Canonical)
		for i := range ev.Combos {
			c := &ev.Combos[i]
			if c.VenueA == pr.VenueA && c.VenueB == pr.VenueB && c.Canonical == want && c.Structure == pr.Structure {
				return c
			}
		}
		return nil
	}
	seen := map[*scan.PaperComboStat]bool{}
	comboNet := 0.0
	if perr == nil {
		for _, pr := range pairs {
			if pr.Canonical == "" || pr.Structure == "" {
				malformed = append(malformed, pr.VenueA+"×"+pr.VenueB)
				continue
			}
			if c := comboOf(pr); c != nil && !seen[c] {
				seen[c] = true
				comboNet += c.AccruedUSD
			}
		}
	}
	netData := fmt.Sprintf("目标组合 $%.4f（本窗合计 $%.4f=在仓 $%.4f+退出 $%.4f）",
		comboNet, ev.WindowNet, ev.NetActive, ev.NetExited)
	if len(malformed) > 0 {
		netData += "；" + strings.Join(malformed, "、") + "：白名单行无品种"
	}
	switch {
	case perr != nil:
		rows = append(rows, StageCheck{Label: "窗内净额非负", OK: false, Data: "组合查询失败:" + perr.Error()})
	case len(pairs) == 0:
		rows = append(rows, StageCheck{Label: "窗内净额非负", OK: false, Data: "白名单无实盘启用组合"})
	default:
		rows = append(rows, StageCheck{
			Label: "窗内净额非负", OK: comboNet >= 0 && len(malformed) == 0, Data: netData})
	}
	// 条件制——白名单实盘组合（D-056 四元组）当前 active 行连续存活
	// ≥NOX_STAGE1_MIN_ROW_HOURS（热读缺省 48）且 accrued>0；退出过的行不计
	//（只查 active）。每个白名单组合都命中才 ✅（fail-closed 覆盖全组合域）。
	minRowHours := cfgIntDef(ctx, d, "NOX_STAGE1_MIN_ROW_HOURS", stage1MinRowHoursDefault)
	active, aerr := d.Store.ListActivePaperSpotCarry(ctx)
	switch {
	case perr != nil:
		rows = append(rows, StageCheck{Label: "目标组合纸面行存活", OK: false, Data: "组合查询失败:" + perr.Error()})
	case aerr != nil:
		rows = append(rows, StageCheck{Label: "目标组合纸面行存活", OK: false, Data: "纸面行查询失败:" + aerr.Error()})
	case len(pairs) == 0:
		rows = append(rows, StageCheck{Label: "目标组合纸面行存活", OK: false, Data: "白名单无实盘启用组合"})
	default:
		allOK := true
		parts := make([]string, 0, len(pairs))
		for _, pr := range pairs {
			if pr.Canonical == "" || pr.Structure == "" {
				allOK = false
				parts = append(parts, pr.VenueA+"×"+pr.VenueB+"：白名单行无品种")
				continue
			}
			want := scan.NormalizeJoinKey(pr.Canonical)
			bestAge, bestAccrued := time.Duration(0), 0.0
			for _, r := range active {
				if r.Status != "active" || r.VenueA != pr.VenueA || r.VenueB != pr.VenueB ||
					r.Symbol != want || r.Structure != pr.Structure {
					continue
				}
				age := now.Sub(r.EntryTs)
				if r.AccruedUSD > 0 && age > bestAge {
					bestAge, bestAccrued = age, r.AccruedUSD // 取龄最长且计息行（组合内多账户任一达标）
				}
			}
			hit := bestAge >= time.Duration(minRowHours)*time.Hour && bestAccrued > 0
			allOK = allOK && hit
			parts = append(parts, fmt.Sprintf("%s %s×%s：%.0fh / $%.2f",
				want, pr.VenueA, pr.VenueB, bestAge.Hours(), bestAccrued))
		}
		rows = append(rows, StageCheck{
			Label: "目标组合纸面行存活", OK: allOK && len(malformed) == 0,
			Data: fmt.Sprintf("%s（需存活 ≥%dh 且计息>0）", strings.Join(parts, " "), minRowHours)})
	}
	// 开仓时点——now 距最近一次 MT 周开盘 ≥ NOX_STAGE1_OPEN_WARMUP_H
	// （热读缺省 2）且当前不在财历禁区（D-053 复用：BlackoutCurrencySet+
	// MacroHighEventsIn，窗=NOX_MACRO_BLACKOUT_MIN 热读缺省 15）。仅就绪表显示项，
	// 不进引擎。会话表缺/查询失败/禁区查询失败 → fail-closed 不 ✅。
	rows = append(rows, stage1OpenTimingCheck(ctx, d, pairs, now))
	incidents, err := d.Store.IncidentCountSince(ctx, since)
	if err != nil {
		rows = append(rows, StageCheck{Label: "期间零事故仓", OK: false, Data: "查询失败:" + err.Error()})
	} else {
		rows = append(rows, StageCheck{Label: "期间零事故仓", OK: incidents == 0, Data: fmt.Sprintf("%d 仓", incidents)})
	}
	fts, fok, ferr := d.Store.LatestFactTs(ctx, fact.KindFunding)
	switch {
	case ferr != nil:
		rows = append(rows, StageCheck{Label: "息差数据在线", OK: false, Data: "查询失败:" + ferr.Error()})
	case !fok:
		rows = append(rows, StageCheck{Label: "息差数据在线", OK: false, Data: "无息差读数"})
	default:
		lag := now.Sub(fts)
		rows = append(rows, StageCheck{Label: "息差数据在线", OK: lag <= fundingFactMaxAge,
			Data: fmt.Sprintf("最新读数 %s 前", lag.Round(time.Minute))})
	}
	rows = append(rows, StageCheck{Label: "纸面计息正常",
		OK:   ev.Stalled == 0,
		Data: fmt.Sprintf("在计 %d 行·卡住 %d 行", ev.ActiveN, ev.Stalled)})
	mig, merr := d.Store.SchemaMigrationMax(ctx)
	if merr != nil {
		rows = append(rows, StageCheck{Label: "数据库版本 ≥172", OK: false, Data: "查询失败:" + merr.Error()})
	} else {
		rows = append(rows, StageCheck{Label: "数据库版本 ≥172", OK: mig >= 172, Data: fmt.Sprintf("当前 %d", mig)})
	}
	killOn, kerr := false, error(nil)
	if d.KillActive != nil {
		killOn, kerr = d.KillActive(ctx)
	}
	kill := "关"
	if killOn {
		kill = "开（已全停）"
	}
	rows = append(rows, StageCheck{Label: "风控总闸在位", OK: kerr == nil,
		Data: "紧急停止=" + kill})
	vc, cc, cerr := d.Store.ExposureCapCounts(ctx)
	if cerr != nil {
		rows = append(rows, StageCheck{Label: "仓位上限已定", OK: false, Data: "查询失败:" + cerr.Error()})
	} else {
		rows = append(rows, StageCheck{Label: "仓位上限已定", OK: vc > 0 && cc > 0,
			Data: fmt.Sprintf("场所 %d 条·品种 %d 条", vc, cc)})
	}
	if perr != nil {
		rows = append(rows, StageCheck{Label: "白名单已定", OK: false, Data: "查询失败:" + perr.Error()})
	} else {
		rows = append(rows, StageCheck{Label: "白名单已定", OK: len(pairs) > 0, Data: fmt.Sprintf("实盘启用 %d 组", len(pairs))})
	}
	// Stage-1 放行接线：就绪告警不再跨停闸报假绿——「执行链可开」=
	// autotrade_state=RUNNING 直读（STRATEGY_EXHAUSTED 全灭停机时如实不就绪）。
	as, aerr := d.Store.GetAutotradeState(ctx)
	if aerr != nil || as == nil {
		rows = append(rows, StageCheck{Label: "执行链可开", OK: false,
			Data: "autotrade_state 读败（停闸面未知不就绪）"})
	} else {
		rows = append(rows, StageCheck{Label: "执行链可开", OK: as.State == "RUNNING",
			Data: "autotrade_state=" + as.State})
	}
	return rows
}

// stage1OpenTimingCheck D-061 ②「开仓时点」行（周开盘暖场+财历禁区，显示项）。
func stage1OpenTimingCheck(ctx context.Context, d Deps, pairs []pgstore.XAssetRealCombo, now time.Time) StageCheck {
	warmup := cfgIntDef(ctx, d, "NOX_STAGE1_OPEN_WARMUP_H", stage1OpenWarmupDefault)
	row := StageCheck{Label: "开仓时点", OK: false}
	startMin, ok, err := d.Store.MinWeekdaySessionStartMin(ctx, fact.KindTradeSession, 1) // 周一
	if err != nil {
		row.Data = "会话查询失败:" + err.Error()
		return row
	}
	if !ok {
		row.Data = "会话表缺周一开盘（trade_session 事实缺席）"
		return row
	}
	// 最近一次周开盘 = 本周（ISO）周一 00:00 UTC + startMin 分钟；未到则回退上周。
	monday := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -int((int(now.UTC().Weekday())+6)%7))
	openAt := monday.Add(time.Duration(startMin) * time.Minute)
	if openAt.After(now) {
		openAt = openAt.AddDate(0, 0, -7)
	}
	elapsed := now.Sub(openAt)
	row.Data = fmt.Sprintf("距周开盘 %.1fh（需 ≥%dh）", elapsed.Hours(), warmup)
	if elapsed < time.Duration(warmup)*time.Hour {
		return row
	}
	// 财历禁区：币种集=NOX_MACRO_BLACKOUT_CURRENCIES（热读缺省 USD）∪ 白名单各 canonical 两半。
	base := cfgStrDef(ctx, d, "NOX_MACRO_BLACKOUT_CURRENCIES", "USD")
	curs := map[string]bool{}
	for _, c := range macro.BlackoutCurrencySet(base, "") {
		curs[c] = true
	}
	for _, pr := range pairs {
		for _, c := range macro.BlackoutCurrencySet("", pr.Canonical) {
			curs[c] = true
		}
	}
	list := make([]string, 0, len(curs))
	for c := range curs {
		list = append(list, c)
	}
	win := cfgIntDef(ctx, d, "NOX_MACRO_BLACKOUT_MIN", 15)
	evs, err := d.Store.MacroHighEventsIn(ctx, now.Add(-time.Duration(win)*time.Minute), now.Add(time.Duration(win)*time.Minute), list)
	if err != nil {
		row.Data += "；禁区查询失败:" + err.Error()
		return row
	}
	if len(evs) > 0 {
		row.Data += fmt.Sprintf("；财历禁区=有（%s %s）", evs[0].Currency, evs[0].Name)
		return row
	}
	row.Data += "；财历禁区=无"
	row.OK = true
	return row
}
