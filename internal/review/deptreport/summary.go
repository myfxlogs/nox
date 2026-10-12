// summary.go —— 经理摘要：十一部报（落库真值重读）合成公司日报 → manager 行落库
// + alerts 留痕 + 飞书扇出。部报全缺=空输入不调用（无可综合面）。
package deptreport

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/store/pgstore"
)

// genManager 经理摘要（Generate 末段；单点失败 fail-open 不影响已落部报）。
func (d *Deps) genManager(ctx context.Context, client *analyst.Client, date time.Time) error {
	reports, err := d.Store.ListDeptReports(ctx, date)
	if err != nil {
		return fmt.Errorf("manager list: %w", err)
	}
	var in strings.Builder
	fmt.Fprintf(&in, "报告日期：%s\n\n", date.Format("2006-01-02"))
	n := 0
	for _, r := range reports {
		if r.Dept == DeptManager { // 同日重跑旧摘要行不入输入
			continue
		}
		n++
		fmt.Fprintf(&in, "=== %s部 ===\n%s\n\n", r.Dept, compact(scrub(r.Body), mgrBodyBudget))
	}
	if n == 0 {
		return fmt.Errorf("manager: 部报全缺（%s）——跳过摘要", date.Format("2006-01-02"))
	}
	if d.Rate != nil && !d.Rate.Allow() {
		return fmt.Errorf("manager: rate limited")
	}
	body, res, ms, err := d.callLLM(ctx, client, managerSystemPrompt, in.String())
	if err != nil {
		return fmt.Errorf("manager llm: %w", err)
	}
	r := pgstore.DeptReport{
		Dept: DeptManager, ReportDate: date, Body: scrub(body),
		ElapsedMs: int(ms), PromptTokens: res.InputTokens, CompletionTokens: res.OutputTokens,
	}
	if err := d.Store.UpsertDeptReport(ctx, r); err != nil {
		return fmt.Errorf("manager upsert: %w", err)
	}
	if d.OnReport != nil {
		d.OnReport(r)
	}
	d.push(ctx, date, r.Body)
	return nil
}

// push 公司日报扇出：alerts info 留痕（看板告警面板可见）+ 飞书直推（复用 macro
// 日历日报形态；Notify/ChatID 任一缺=只留痕不推，fail-open 两级）。
func (d *Deps) push(ctx context.Context, date time.Time, body string) {
	text := "【NOX 公司日报 " + date.Format("01-02") + "】\n" + body
	if err := d.Store.InsertAlertDedup(ctx, "info", text, 4*time.Hour); err != nil {
		log.Printf("[deptreport] alert: %v", err)
	}
	if d.Notify == nil || !d.Notify.Enabled() || d.ChatID == nil {
		return
	}
	chatID, err := d.ChatID(ctx)
	if err != nil {
		log.Printf("[deptreport] chat id: %v", err)
		return
	}
	if chatID == "" {
		return
	}
	if _, err := d.Notify.SendText(ctx, chatID, text); err != nil {
		log.Printf("[deptreport] feishu push: %v", err)
	}
}
