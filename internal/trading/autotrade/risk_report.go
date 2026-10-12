package autotrade

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/risk/gate"
	"arb/internal/store/pgstore"
)

// risk_report.go —— D-032 ④（D-030）：每周一 08:00 BJ 经营周报。
// 文案单点 BuildRiskReport（快照测试锚）；发送/调度在 cmd/core wire_risk_report.go。

// riskReportWindow 周报滚动窗（与 dd30 同窗）。
const riskReportWindow = riskDD30Window

// riskReportTargetPct D-030 净年化目标线。
const riskReportTargetPct = 8.0

// BuildRiskReport 周报文案。全样本/非事故样本两列年化（D-030 口径：单源账本
// position_settlements 30d 净额→年化为 net/30×365/base）。任一数据查询失败
// → error（发送方记日志不发——哑报比缺报更糟）。
func BuildRiskReport(ctx context.Context, st *pgstore.Store, cp *gate.ControlPlane, capitalBaseUSD float64, now time.Time) (string, error) {
	since := now.Add(-riskReportWindow)
	netAll, err := st.NetRealizedSince(ctx, since, false)
	if err != nil {
		return "", fmt.Errorf("report net all: %w", err)
	}
	netClean, err := st.NetRealizedSince(ctx, since, true)
	if err != nil {
		return "", fmt.Errorf("report net clean: %w", err)
	}
	actives, err := st.ListActivePositions(ctx)
	if err != nil {
		return "", fmt.Errorf("report actives: %w", err)
	}
	incidents, err := st.IncidentCountSince(ctx, since)
	if err != nil {
		return "", fmt.Errorf("report incidents: %w", err)
	}
	// 探针成本单列（Σ type='PROBE' 现金流 30d——入账本但不入上述净额行）。
	probeCost, err := st.ProbeCashflowSumSince(ctx, since)
	if err != nil {
		return "", fmt.Errorf("report probe cost: %w", err)
	}
	killOn, err := cp.IsKillSwitchActive(ctx)
	if err != nil {
		return "", fmt.Errorf("report kill state: %w", err)
	}
	var notional float64
	for _, p := range actives {
		if p.NotionalUSD != nil {
			notional += *p.NotionalUSD
		}
	}
	kill := "正常"
	if killOn {
		kill = "已触发（全停）"
	}
	ann := func(net float64) float64 {
		if capitalBaseUSD <= 0 {
			return 0
		}
		return net / riskReportWindow.Hours() * 24 * 365 / capitalBaseUSD * 100
	}
	mark := func(a float64) string {
		if a >= riskReportTargetPct {
			return "≥8% ✓"
		}
		return "<8% ✗"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "【ARB 周报】%s BJ\n", now.In(riskReportLoc).Format("2006-01-02"))
	fmt.Fprintf(&b, "基数 $%.0f｜目标净年化 ≥%.0f%%\n", capitalBaseUSD, riskReportTargetPct)
	fmt.Fprintf(&b, "30d 账本净额（全样本） $%.2f → 年化 %.2f%%（%s）\n", netAll, ann(netAll), mark(ann(netAll)))
	fmt.Fprintf(&b, "30d 账本净额（剔事故） $%.2f → 年化 %.2f%%（%s）\n", netClean, ann(netClean), mark(ann(netClean)))
	fmt.Fprintf(&b, "在仓 %d 仓｜名义 $%.0f\n", len(actives), notional)
	fmt.Fprintf(&b, "事故（30d） %d｜KillSwitch %s\n", incidents, kill)
	fmt.Fprintf(&b, "探针成本（30d Σ PROBE 现金流） $%.2f\n", probeCost)
	return b.String(), nil
}

var riskReportLoc = func() *time.Location {
	l, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return l
}()

// NextRiskReportAt 下周一 08:00 BJ（now 之后第一个周一定点）。
// 调度器用 time.Timer 睡到该时刻——周报是 cron 类定时任务（同 repoReminder/
// calib 先例），非业务数据刷新 ticker。
func NextRiskReportAt(now time.Time) time.Time {
	bj := now.In(riskReportLoc)
	d := time.Date(bj.Year(), bj.Month(), bj.Day(), 8, 0, 0, 0, riskReportLoc)
	for !d.After(now) || d.Weekday() != time.Monday {
		d = d.AddDate(0, 0, 1)
	}
	return d
}
