// run.go —— 日频编排：07:05 BJ 触发，报告日=前一 BJ 历日（digest 06:10/explorer
// 06:47 之后取新鲜数据面；selfwatch 08:00 汇报前出公司日报）。
// 顺序：逐十一部（账本行=财务专项）→ 经理摘要（manager 行）→ 飞书扇出。
package deptreport

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/store/pgstore"
)

// deptSystemPrompt 部报系统约束（数字只引用输入段原值——禁编数/禁重算）。
const deptSystemPrompt = `你是 NOX 套利系统的部门日报撰写员。输入=Go 确定性查询结果分段（名册/运营盘/持仓/财务专项）。
输出=该部门当日日报（≤400 字纯文本，无 markdown 标题）。
结构：①本部员工状态一句话；②本部关注面要点（按输入段「关注」行引导）；③异常/待办（无则写「无」）。
红线：数字只能引用输入段原值，禁止自算/重算/编造；查不到的数据写「未采」；
不给交易/仓位指令——本报告仅观察+建议面，不进入风控与执行路径。`

// managerSystemPrompt 经理摘要系统约束（输入=十一部报正文，不碰原始数字表）。
const managerSystemPrompt = `你是 NOX 套利系统的总经理。输入=十一部当日部报正文（含账本财务日报）。
输出=公司日报（≤600 字纯文本，无 markdown 标题）。
结构：①全司一句话定调；②按部归纳要点（合并同类，不逐部复读）；③财务一句话；④异常与明日关注。
红线：只能引用各部报正文已写数字，禁止补写未出现的数字；不给交易/仓位指令。`

// NextDelay 距下一触发时刻（每日 07:05 BJ；now 恰过则取次日）。
func NextDelay(now time.Time) time.Duration {
	bj := now.In(BJTime)
	next := time.Date(bj.Year(), bj.Month(), bj.Day(), 7, 5, 0, 0, BJTime)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(now)
}

// ReportDate 报告日=now 前一 BJ 历日（钉 UTC 零时值，DB DATE 写入不赌时区）。
func ReportDate(now time.Time) time.Time {
	bj := now.In(BJTime).AddDate(0, 0, -1)
	return time.Date(bj.Year(), bj.Month(), bj.Day(), 0, 0, 0, 0, time.UTC)
}

// sleepUntil ctx 感知睡眠（var=测试注入点，同 macro 先例）。
var sleepUntil = func(ctx context.Context, t time.Time) bool {
	d := time.Until(t)
	if d <= 0 {
		return true
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-tm.C:
		return true
	}
}

// Run 日频阻塞循环（safego 托管；fail-open——整班/单部失败只 log 不阻塞）。
// 先睡到下一触发点再跑——重启不补班（错过班次由次日正常班覆盖，防重启风暴刷 LLM）。
func Run(ctx context.Context, d Deps) {
	d.fill()
	for {
		next := d.Now().Add(NextDelay(d.Now()))
		if !sleepUntil(ctx, next) {
			return
		}
		if d.Beat != nil {
			d.Beat()
		}
		if d.Enabled != nil && !d.Enabled() {
			log.Printf("[deptreport] disabled 当班跳过")
			continue
		}
		if err := Generate(ctx, &d, ReportDate(d.Now())); err != nil {
			log.Printf("[deptreport] generate: %v", err)
		}
	}
}

// Generate 生成一日全套（date=报告日 UTC 零时；窗=该 BJ 历日）。
// 手动触发/测试同入口。返回仅「编排级」错（store 读面全挂）；单部失败不返回。
func Generate(ctx context.Context, d *Deps, date time.Time) error {
	d.fill()
	client, err := d.resolveAI(ctx)
	if err != nil {
		return fmt.Errorf("resolve client: %w", err)
	}
	if client == nil {
		return fmt.Errorf("AI 未配置（app_secrets NOX_AI_* 或 env 双缺）——当班跳过")
	}
	// 报告日=UTC 零时钉的 BJ 历日 → 统计窗=[BJ 当日 00:00, 次日 00:00)（RetroDaily
	// UTC 界由调用方给——DB timezone=Etc/UTC 不赌 DB 侧）。
	bj := date.In(BJTime)
	dayStart := time.Date(bj.Year(), bj.Month(), bj.Day(), 0, 0, 0, 0, BJTime)
	dayEnd := dayStart.AddDate(0, 0, 1)
	var done int
	for _, dept := range pgstore.EmployeeDepts {
		if err := d.genDept(ctx, client, dept, date, dayStart, dayEnd); err != nil {
			log.Printf("[deptreport] dept=%s: %v", dept, err) // fail-open 续跑
			continue
		}
		done++
	}
	log.Printf("[deptreport] depts ok=%d/%d date=%s", done, len(pgstore.EmployeeDepts), date.Format("2006-01-02"))
	return d.genManager(ctx, client, date)
}

// genDept 单部：gather 输入段 → LLM 综合 → 落库（Rate 占一令牌，超限跳过记 log）。
func (d *Deps) genDept(ctx context.Context, client *analyst.Client, dept string, date, dayStart, dayEnd time.Time) error {
	if d.Rate != nil && !d.Rate.Allow() {
		return fmt.Errorf("rate limited")
	}
	input, err := gather(ctx, d, dept, dayStart, dayEnd)
	if err != nil {
		return err
	}
	body, res, ms, err := d.callLLM(ctx, client, deptSystemPrompt, input)
	if err != nil {
		return err
	}
	r := pgstore.DeptReport{
		Dept: dept, ReportDate: date, Body: body,
		ElapsedMs: int(ms), PromptTokens: res.InputTokens, CompletionTokens: res.OutputTokens,
	}
	if err := d.Store.UpsertDeptReport(ctx, r); err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	if d.OnReport != nil {
		d.OnReport(r)
	}
	return nil
}

// callLLM 单次部报/摘要调用（无工具面——数字全在输入段，LLM 纯综合）。
func (d *Deps) callLLM(ctx context.Context, client *analyst.Client, system, user string) (string, analyst.LoopResult, int64, error) {
	msgs := []analyst.Message{analyst.TextContent("user", user)}
	var out string
	start := time.Now()
	res, err := d.Run(ctx, client, d.Tools, nil, system, msgs, analyst.LoopCallbacks{
		OnDelta: func(t string) { out += t },
	})
	if err != nil {
		return "", res, 0, err
	}
	return out, res, time.Since(start).Milliseconds(), nil
}
