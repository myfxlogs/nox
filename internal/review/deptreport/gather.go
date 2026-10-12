// gather.go —— 部报输入段装配（全确定性：数字只来自 PG 查询结果渲染文本，
// LLM 侧 prompt 禁编数纪律见 deps 系统提示）。
package deptreport

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// deptFocus 部报专属关注段——键=十一部中文名（pgstore.EmployeeDepts 同域），
// 值=输入段名。通用运营盘段所有部共享；专属段引导 LLM 综合方向（数字仍全给）。
var deptFocus = map[string]string{
	"数据": "关注：报价流/采集健康（断流事件、源健康）与数据完整性。",
	"研究": "关注：探索产出与毕业建议（graduations）、机会供给质量。",
	"投资": "关注：机会漏斗（开仓/平仓节奏）与策略分布 EvalDist。",
	"交易": "关注：当日开/平仓与 OPEN 持仓 carry 实收明细。",
	"风控": "关注：告警分布/Top3 与流事件，硬线是否触碰。",
	"运营": "关注：账户/venue 资金池与运维事件面。",
	"账本": "关注：财务日报专项（收入构成/已实现 PnL/venue 资金池/手工收入行）。",
	"复盘": "关注：闭环质量——告警聚类、开平仓效率、报告产出。",
	"IR": "关注：对外通报面——告警级别构成与业主可读要点。",
	"运维": "关注：部署与流稳定性（断流/重连事件计数）。",
	"合规": "关注：纪律面——critical 告警与异常行为审查。",
}

// rosterBlock 部门名册段：该部员工状态行（在职/滞跳/离线/离职由后端 EmployeeStatus 算定）。
func rosterBlock(dept string, emps []pgstore.Employee, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【%s部·员工状态】\n", dept)
	n := 0
	for _, e := range emps {
		if e.Dept != dept {
			continue
		}
		n++
		task := e.Task
		if task == "" {
			task = "-"
		}
		fmt.Fprintf(&b, "- %s（%s）：%s｜装配=%s｜任务=%s\n", e.Name, e.Role, e.EmployeeStatus(now), e.Assembly, task)
	}
	if n == 0 {
		b.WriteString("- （本部无在册员工）\n")
	}
	return b.String()
}

// opsBlock 全司运营盘段（RetroDaily 原值渲染；窗口=BJ 历日）。
func opsBlock(rd *pgstore.RetroDaily) string {
	var b strings.Builder
	b.WriteString("【全司运营盘】\n")
	fmt.Fprintf(&b, "- OPEN 持仓=%d｜当日开仓=%d｜当日平仓=%d｜毕业建议=%d\n",
		len(rd.OpenPos), rd.OpenedToday, rd.ClosedToday, rd.GraduationsToday)
	fmt.Fprintf(&b, "- funding 实收=%.2f USD｜funding 预估=%.2f USD｜流事件=%d\n",
		rd.FundingActualUSD, rd.FundingEstUSD, rd.StreamEvents)
	b.WriteString("- 告警分布：" + distLine(rd.AlertDist) + "\n")
	b.WriteString("- 评估分布：" + distLine(rd.EvalDist) + "\n")
	for i, m := range rd.AlertTop3 {
		if i >= 3 {
			break
		}
		fmt.Fprintf(&b, "- 告警Top%d：%s\n", i+1, m)
	}
	return b.String()
}

// openPosBlock OPEN 持仓明细段（交易/风控/账本共享）。
func openPosBlock(rd *pgstore.RetroDaily) string {
	var b strings.Builder
	b.WriteString("【OPEN 持仓明细】\n")
	if len(rd.OpenPos) == 0 {
		b.WriteString("- （无）\n")
		return b.String()
	}
	for _, p := range rd.OpenPos {
		fmt.Fprintf(&b, "- #%d %s @%s｜carry实收=%.2f USD｜开仓=%s\n",
			p.ID, p.Type, p.Venue, p.CarryUSD, p.OpenedAt.Format("01-02 15:04"))
	}
	return b.String()
}

// financeBlock 账本专项段（D-013 单源账本口径：PnL/收入构成/venue 资金池）。
func financeBlock(pnls []pgstore.FinancePnlEnv, incomes []pgstore.FinanceIncomeKind, snaps []pgstore.VenueAccountLatest) string {
	var b strings.Builder
	b.WriteString("【财务专项·已实现PnL按环境】\n")
	if len(pnls) == 0 {
		b.WriteString("- （无已实现平仓）\n")
	}
	for _, p := range pnls {
		fmt.Fprintf(&b, "- %s：realized=%.2f USD（平仓 %d 笔）\n", p.Environment, p.RealizedPnl, p.Closed)
	}
	b.WriteString("【财务专项·收入构成（累计）】\n")
	if len(incomes) == 0 {
		b.WriteString("- （无收入行）\n")
	}
	for _, inc := range incomes {
		fmt.Fprintf(&b, "- %s：%.2f USD（%d 行）\n", inc.Kind, inc.TotalUSD, inc.Rows)
	}
	b.WriteString("【财务专项·venue 资金池快照】\n")
	if len(snaps) == 0 {
		b.WriteString("- （无快照行）\n")
	}
	for _, s := range snaps {
		fmt.Fprintf(&b, "- %s/%s：balance=%s %s｜equity=%s｜free_margin=%s｜buffer=%s｜抓取=%s\n",
			s.Venue, s.VenueType, f2s(s.BalanceRaw), s.Currency, f2s(s.EquityRaw), f2s(s.FreeMarginRaw), f2s(s.BufferMultiple), s.FetchedAt.Format("01-02 15:04"))
	}
	return b.String()
}

// gather 装配部报输入：名册段+运营盘+OPEN 明细+（账本=财务专项段）。
func gather(ctx context.Context, d *Deps, dept string, dayStart, dayEnd time.Time) (string, error) {
	rd, err := d.Store.RetroDailyStats(ctx, dayStart, dayEnd)
	if err != nil {
		return "", fmt.Errorf("retro daily: %w", err)
	}
	emps, err := d.Store.ListEmployees(ctx)
	if err != nil {
		return "", fmt.Errorf("employees: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "报告日期：%s（北京时间历日）\n部门：%s\n关注：%s\n\n",
		dayStart.In(BJTime).Format("2006-01-02"), dept, deptFocus[dept])
	b.WriteString(rosterBlock(dept, emps, d.Now()) + "\n")
	b.WriteString(opsBlock(rd) + "\n")
	b.WriteString(openPosBlock(rd) + "\n")
	if dept == "账本" {
		pnls, err := d.Store.FinancePnlByEnv(ctx)
		if err != nil {
			return "", fmt.Errorf("finance pnl: %w", err)
		}
		incomes, err := d.Store.FinanceIncomeByKind(ctx)
		if err != nil {
			return "", fmt.Errorf("finance income: %w", err)
		}
		snaps, err := d.Store.LatestVenueAccountSnapshots(ctx)
		if err != nil {
			return "", fmt.Errorf("venue snaps: %w", err)
		}
		b.WriteString(financeBlock(pnls, incomes, snaps))
	}
	return b.String(), nil
}

// distLine map 分布渲染（键序稳定——输出确定性，报告 diff 可审）。
func distLine(m map[string]int) string {
	if len(m) == 0 {
		return "（无）"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// f2s 可空 float 渲染（nil=「未采」诚实占位）。
func f2s(v *float64) string {
	if v == nil {
		return "未采"
	}
	return fmt.Sprintf("%.2f", *v)
}
