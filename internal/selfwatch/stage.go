// stage.go —— D-033②：Stage 闸就绪条件表（每项=标签+判定+实测数据串）。
// 口径来源：Stage 1=decisions D-023/D-025/D-030（paper ≥7d 净额非负·零事故·
// 子系统在线·cap/白名单）；Stage 2=D-030 账本 30d 年化 ≥8% + D-026 maker 分级
// （≥20 笔 maker 开仓零事故）。
// D-056①②：组合证据=白名单行精确配对键（venue_a×venue_b×canonical×structure，
// 同 venue 对多品种不串账）；全表文案面向运营——零票号/内部实现词。
package selfwatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/scan"
	"arb/internal/store/pgstore"
)

// paperStageDays Stage 1 paper 窗最短期龄（D-023 口径 ≥7 天）。
const paperStageDays = 7 * 24 * time.Hour

// fundingFactMaxAge D-027 在线判据——funding facts 流供体最大龄（mark-price WS
// 持续推送，超龄=流死=滚动均值无源）。
const fundingFactMaxAge = 4 * time.Hour

// makerStageOpens D-026 真金分级门槛：≥20 笔 maker 开仓零事故 → Stage 2 可带 maker。
const makerStageOpens = 20

// stageTargetPct D-030 净年化目标线。
const stageTargetPct = 8.0

// stage1ComboDays Stage 1 组合证据：目标组合（白名单实盘启用 venue 对）窗内
// 累计在仓天数下限——多行时按各行 [entry,exit|now]∩窗 时长求和。
const stage1ComboDays = 5.0

func stage1Checks(ctx context.Context, d Deps, since time.Time, windowOK bool, ev *scan.PaperWindowEval, now time.Time) []StageCheck {
	rows := []StageCheck{}
	if !windowOK || ev == nil {
		return []StageCheck{
			{Label: "纸面观测窗已配置", OK: false, Data: "窗起点未配置（ARB_PAPER_WINDOW_START）"},
		}
	}
	age := now.Sub(since)
	rows = append(rows, StageCheck{
		Label: "纸面观测窗 ≥7 天", OK: age >= paperStageDays,
		Data: fmt.Sprintf("已观测 %.1f 天（起点 %s）", age.Hours()/24, since.UTC().Format("01-02 15:04Z")),
	})
	// D-056①：组合域=白名单实盘启用行的精确配对键（venue_a×venue_b×canonical×
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
	switch {
	case perr != nil:
		rows = append(rows, StageCheck{Label: "Stage 1 组合证据", OK: false, Data: "组合查询失败:" + perr.Error()})
	case len(pairs) == 0:
		rows = append(rows, StageCheck{Label: "Stage 1 组合证据", OK: false, Data: "白名单无实盘启用组合"})
	default:
		ok := false
		parts := make([]string, 0, len(pairs))
		for _, pr := range pairs {
			if pr.Canonical == "" || pr.Structure == "" {
				parts = append(parts, pr.VenueA+"×"+pr.VenueB+"：白名单行无品种")
				continue
			}
			c := comboOf(pr)
			if c == nil {
				parts = append(parts, fmt.Sprintf("%s %s×%s：无窗内行",
					scan.NormalizeJoinKey(pr.Canonical), pr.VenueA, pr.VenueB))
				continue
			}
			if c.ActiveDays >= stage1ComboDays && c.AccruedUSD > 0 {
				ok = true
			}
			parts = append(parts, fmt.Sprintf("%s %s×%s：%.1fd / $%.2f",
				c.Canonical, c.VenueA, c.VenueB, c.ActiveDays, c.AccruedUSD))
		}
		rows = append(rows, StageCheck{
			Label: "Stage 1 组合证据", OK: ok && len(malformed) == 0,
			Data: fmt.Sprintf("%s（需 ≥%gd 且净额>0）", strings.Join(parts, " "), stage1ComboDays)})
	}
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
	return rows
}

func stage2Checks(ctx context.Context, d Deps, since time.Time, now time.Time) []StageCheck {
	since30 := now.Add(-30 * 24 * time.Hour)
	net, err := d.Store.NetRealizedSince(ctx, since30, false)
	ann := 0.0
	if d.CapitalBase > 0 {
		ann = net / 30 * 365 / d.CapitalBase * 100
	}
	row1 := StageCheck{Label: "近 30 天真金账本", OK: ann >= stageTargetPct,
		Data: fmt.Sprintf("净 $%.2f → 年化 %.2f%%（目标 ≥8%%；基数 $%.0f）", net, ann, d.CapitalBase)}
	if err != nil {
		row1 = StageCheck{Label: "近 30 天真金账本", OK: false, Data: "查询失败:" + err.Error()}
	}
	ack, _, aerr := d.Store.AppConfigInt64(ctx, "ARB_RISK_INCIDENT_ACK_ID")
	blocked, berr := int64(0), error(nil)
	if aerr == nil {
		blocked, berr = d.Store.IncidentBlockerID(ctx, ack)
	}
	row2 := StageCheck{Label: "事故仓已清零", OK: berr == nil && blocked == 0}
	switch {
	case aerr != nil:
		row2.Data = "事故确认查询失败:" + aerr.Error()
	case berr != nil:
		row2.Data = "查询失败:" + berr.Error()
	default:
		row2.Data = fmt.Sprintf("已确认到 #%d｜待确认 %d 仓", ack, blocked)
	}
	makerOn := false
	if v, ok, merr := d.Store.AppConfigString(ctx, "ARB_MAKER_TIMEOUT"); merr == nil && ok {
		if dv, derr := time.ParseDuration(v); derr == nil {
			makerOn = dv > 0
		}
	}
	opens, minc, merr := d.Store.MakerLegStatsSince(ctx, since)
	row3 := StageCheck{Label: "挂单模式",
		OK: makerOn && opens >= makerStageOpens && minc == 0}
	if merr != nil {
		row3.Data = "查询失败:" + merr.Error()
	} else if makerOn {
		row3.Data = fmt.Sprintf("已启用（开仓 %d 次·事故 %d）", opens, minc)
	} else {
		row3.Data = fmt.Sprintf("未启用（需 ≥%d 次零事故开仓后放行；当前 %d 次·事故 %d）", makerStageOpens, opens, minc)
	}
	return []StageCheck{row1, row2, row3}
}
