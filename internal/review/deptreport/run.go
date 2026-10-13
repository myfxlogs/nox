// run.go —— 日频编排：07:05 BJ 触发，报告日=前一 BJ 历日（digest 06:10/explorer
// 06:47 之后取新鲜数据面）。D-246 v3：11 部报+经理摘要→1 次公司日报 1 次 LLM
// 调用；fail-open——整班失败只 log 不阻塞业务；重启不补班防刷 LLM。
package deptreport

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/store/pgstore"
)

// companySystemPrompt 公司日报系统约束（数字只引用输入段原值——禁编数/禁重算）。
const companySystemPrompt = `你是 NOX 套利系统的公司日报撰写员。输入=Go 确定性查询结果（money-first 固定序七段+必备指标行）。
输出=公司日报（≤600 字纯文本，无 markdown 标题）。
结构：按输入段固定序归纳——①NAV 与闲置；②在仓与 carry；③待决提案（点出超龄者）；④请求箱（投资人侧/系统侧分列）；⑤策略态变；⑥事故与硬线；⑦流健康。末行「明日关注」一句。
红线：数字只能引用输入段原值，禁止自算/重算/编造；查不到的数据写「未采」；
venue/交易所名一律用输入段英文原词（binance/okx/gate/mt5），禁翻中文译名；
禁止出现「员工/在职/离职/考勤」类组织措辞；禁止向业主/投资人发出动作指令或建议
（报告仅观察+建议面，不进入风控与执行路径，不给交易/仓位指令）。`

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

// CutDayStart 日切日=now 当日 BJ 历日零时（UTC 值；fin_nav_daily.day_start 口径）。
func CutDayStart(now time.Time) time.Time {
	bj := now.In(BJTime)
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

// Run 日频阻塞循环（safego 托管）。先睡到下一触发点再跑——重启不补班
// （错过班次由次日正常班覆盖，防重启风暴刷 LLM）。
func Run(ctx context.Context, d Deps) {
	d.fill()
	// M4.4①：boot 首切——fin_nav_daily/fin_strategy_budget 幂等落行，
	// 防「配置器未产行→BudgetChecker 全单拒」的首日窗口。
	if d.OnDayCut != nil {
		if err := d.OnDayCut(ctx, CutDayStart(d.Now())); err != nil {
			log.Printf("[deptreport] day cut boot: %v", err)
		}
	}
	for {
		next := d.Now().Add(NextDelay(d.Now()))
		if !sleepUntil(ctx, next) {
			return
		}
		if d.Beat != nil {
			d.Beat()
		}
		if d.OnDayCut != nil {
			if err := d.OnDayCut(ctx, CutDayStart(d.Now())); err != nil {
				log.Printf("[deptreport] day cut: %v", err)
			}
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

// Generate 生成一日公司日报（date=报告日 UTC 零时；窗=该 BJ 历日）：
// gather 七段输入 → 1 次 LLM 综合 → dept='company' 行落库 → 推送。
// 手动触发/测试同入口。失败即返错（零半成品落库）。
func Generate(ctx context.Context, d *Deps, date time.Time) error {
	d.fill()
	client, err := d.resolveAI(ctx)
	if err != nil {
		return fmt.Errorf("resolve client: %w", err)
	}
	if client == nil {
		return fmt.Errorf("AI 未配置（app_secrets NOX_AI_* 或 env 双缺）——当班跳过")
	}
	bj := date.In(BJTime)
	dayStart := time.Date(bj.Year(), bj.Month(), bj.Day(), 0, 0, 0, 0, BJTime)
	dayEnd := dayStart.AddDate(0, 0, 1)
	if d.Rate != nil && !d.Rate.Allow() {
		return fmt.Errorf("rate limited")
	}
	input, err := gather(ctx, d, dayStart, dayEnd)
	if err != nil {
		return err
	}
	body, res, ms, err := d.callLLM(ctx, client, companySystemPrompt, scrub(input))
	if err != nil {
		return err
	}
	r := pgstore.DeptReport{
		Dept: DeptCompany, ReportDate: date, Body: scrubBody(body),
		ElapsedMs: int(ms), PromptTokens: res.InputTokens, CompletionTokens: res.OutputTokens,
	}
	if err := d.Store.UpsertDeptReport(ctx, r); err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	if d.OnReport != nil {
		d.OnReport(r)
	}
	d.push(ctx, date, r.Body)
	return nil
}

// callLLM 单次公司日报调用（无工具面——数字全在输入段，LLM 纯综合）。
// 重试一次：上游内容安全闸为概率判分——边界输入同文重发可放行（1301 实证
// 2026-10-13：同输入前后两次一挂一过）；二次仍挂走上层 fail-open。
func (d *Deps) callLLM(ctx context.Context, client *analyst.Client, system, user string) (string, analyst.LoopResult, int64, error) {
	msgs := []analyst.Message{analyst.TextContent("user", user)}
	var res analyst.LoopResult
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", res, 0, ctx.Err()
			case <-time.After(2 * time.Second):
			}
			log.Printf("[deptreport] llm 重试（首轮: %v）", err)
		}
		var out string
		start := time.Now()
		res, err = d.Run(ctx, client, d.Tools, nil, system, msgs, analyst.LoopCallbacks{
			OnDelta: func(t string) { out += t },
		})
		if err == nil {
			return out, res, time.Since(start).Milliseconds(), nil
		}
	}
	return "", res, 0, err
}
