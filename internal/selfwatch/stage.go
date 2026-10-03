// stage.go —— D-033②：Stage 闸就绪条件表（每项=标签+判定+实测数据串）。
// 口径来源：Stage 1=decisions D-023/D-025/D-030（paper ≥7d 净额非负·零事故·
// 子系统在线·cap/白名单）；Stage 2=D-030 账本 30d 年化 ≥8% + D-026 maker 分级
// （≥20 笔 maker 开仓零事故）。
package selfwatch

import (
	"context"
	"fmt"
	"time"

	"arb/internal/fact"
	"arb/internal/scan"
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

func stage1Checks(ctx context.Context, d Deps, since time.Time, windowOK bool, ev *scan.PaperWindowEval, now time.Time) []StageCheck {
	rows := []StageCheck{}
	if !windowOK || ev == nil {
		return []StageCheck{
			{Label: "paper 窗已配置", OK: false, Data: "ARB_PAPER_WINDOW_START 缺"},
		}
	}
	age := now.Sub(since)
	rows = append(rows, StageCheck{
		Label: "paper 窗 ≥7 天", OK: age >= paperStageDays,
		Data: fmt.Sprintf("窗龄 %.1fd（起点 %s）", age.Hours()/24, since.UTC().Format("01-02 15:04Z")),
	})
	rows = append(rows, StageCheck{
		Label: "窗内净额非负", OK: ev.WindowNet >= 0,
		Data: fmt.Sprintf("$%.4f（在仓 %.4f + 退出 %.4f）", ev.WindowNet, ev.NetActive, ev.NetExited),
	})
	incidents, err := d.Store.IncidentCountSince(ctx, since)
	if err != nil {
		rows = append(rows, StageCheck{Label: "期间零事故仓", OK: false, Data: "查询失败:" + err.Error()})
	} else {
		rows = append(rows, StageCheck{Label: "期间零事故仓", OK: incidents == 0, Data: fmt.Sprintf("%d 仓", incidents)})
	}
	fts, fok, ferr := d.Store.LatestFactTs(ctx, fact.KindFunding)
	switch {
	case ferr != nil:
		rows = append(rows, StageCheck{Label: "D-027 在线（carry 均值口径）", OK: false, Data: "查询失败:" + ferr.Error()})
	case !fok:
		rows = append(rows, StageCheck{Label: "D-027 在线（carry 均值口径）", OK: false, Data: "funding facts 零行"})
	default:
		lag := now.Sub(fts)
		rows = append(rows, StageCheck{Label: "D-027 在线（carry 均值口径）", OK: lag <= fundingFactMaxAge,
			Data: fmt.Sprintf("最新 funding fact %s 前", lag.Round(time.Minute))})
	}
	rows = append(rows, StageCheck{Label: "D-028 在线（paper accrue）",
		OK:   ev.ActiveN > 0 && ev.FailClosed == 0 && ev.Stalled == 0,
		Data: fmt.Sprintf("active=%d failClosed=%d stall=%d", ev.ActiveN, ev.FailClosed, ev.Stalled)})
	mig, merr := d.Store.SchemaMigrationMax(ctx)
	killOn, kerr := false, error(nil)
	if d.KillActive != nil {
		killOn, kerr = d.KillActive(ctx)
	}
	switch {
	case merr != nil:
		rows = append(rows, StageCheck{Label: "D-032 在线（风控硬线）", OK: false, Data: "迁移查询失败:" + merr.Error()})
	case kerr != nil:
		rows = append(rows, StageCheck{Label: "D-032 在线（风控硬线）", OK: false, Data: "kill 查询失败:" + kerr.Error()})
	default:
		kill := "off"
		if killOn {
			kill = "on（已全停）"
		}
		rows = append(rows, StageCheck{Label: "D-032 在线（风控硬线）", OK: mig >= 172,
			Data: fmt.Sprintf("migrations=%d kill=%s", mig, kill)})
	}
	vc, cc, cerr := d.Store.ExposureCapCounts(ctx)
	if cerr != nil {
		rows = append(rows, StageCheck{Label: "cap 已定", OK: false, Data: "查询失败:" + cerr.Error()})
	} else {
		rows = append(rows, StageCheck{Label: "cap 已定", OK: vc > 0 && cc > 0,
			Data: fmt.Sprintf("venue %d 行·canonical %d 行", vc, cc)})
	}
	wl, werr := d.Store.XAssetAllowRealCount(ctx)
	if werr != nil {
		rows = append(rows, StageCheck{Label: "白名单已定", OK: false, Data: "查询失败:" + werr.Error()})
	} else {
		rows = append(rows, StageCheck{Label: "白名单已定", OK: wl > 0, Data: fmt.Sprintf("实盘启用 %d 对", wl)})
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
	row1 := StageCheck{Label: "30d 账本年化 ≥8%", OK: ann >= stageTargetPct,
		Data: fmt.Sprintf("$%.2f→%.2f%%（基数 $%.0f）", net, ann, d.CapitalBase)}
	if err != nil {
		row1 = StageCheck{Label: "30d 账本年化 ≥8%", OK: false, Data: "查询失败:" + err.Error()}
	}
	ack, _, aerr := d.Store.AppConfigInt64(ctx, "ARB_RISK_INCIDENT_ACK_ID")
	blocked, berr := int64(0), error(nil)
	if aerr == nil {
		blocked, berr = d.Store.IncidentBlockerID(ctx, ack)
	}
	row2 := StageCheck{Label: "零事故仓（未 ack）", OK: berr == nil && blocked == 0}
	switch {
	case aerr != nil:
		row2.Data = "ack 查询失败:" + aerr.Error()
	case berr != nil:
		row2.Data = "查询失败:" + berr.Error()
	default:
		row2.Data = fmt.Sprintf("ack=%d 未决=%d", ack, blocked)
	}
	makerOn := false
	if v, ok, merr := d.Store.AppConfigString(ctx, "ARB_MAKER_TIMEOUT"); merr == nil && ok {
		if dv, derr := time.ParseDuration(v); derr == nil {
			makerOn = dv > 0
		}
	}
	opens, minc, merr := d.Store.MakerLegStatsSince(ctx, since)
	row3 := StageCheck{Label: "maker 分级（≥20 开仓零事故）",
		OK: makerOn && opens >= makerStageOpens && minc == 0}
	if merr != nil {
		row3.Data = "查询失败:" + merr.Error()
	} else {
		row3.Data = fmt.Sprintf("启用=%v maker 开仓 %d/事故 %d", makerOn, opens, minc)
	}
	return []StageCheck{row1, row2, row3}
}
