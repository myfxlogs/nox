// stage2.go —— D-033②：Stage 2 条件表（D-030 账本 30d 年化 ≥8% + 事故清零 +
// maker 分级）。D-061 拆出自 stage.go（纯搬移零逻辑变更，行数合规）。
package selfwatch

import (
	"context"
	"fmt"
	"time"
)

// stageTargetPct D-030 净年化目标线。
const stageTargetPct = 8.0

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
	ack, _, aerr := d.Store.AppConfigInt64(ctx, "NOX_RISK_INCIDENT_ACK_ID")
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
	if v, ok, merr := d.Store.AppConfigString(ctx, "NOX_MAKER_TIMEOUT"); merr == nil && ok {
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
