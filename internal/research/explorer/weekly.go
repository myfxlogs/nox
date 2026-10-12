// weekly.go 周深挖（D-513 §2/§5）：周序号 % 静态主题表选题 → 一次深挖调用 →
// docs/研究/EXPLORER-<主题>-<日期>-LLM.md 幂等落盘；LLM 无 git 权。
package explorer

import (
	"context"
	"fmt"
	"strings"

	"nox/internal/platform/analyst"
)

// weeklyThemes 静态主题表（设计稿 §3 五项；周序号取模轮转）。
var weeklyThemes = []string{
	"期限离散", "新符号链", "毕业链裁决", "公告规则面", "资金池搬运", // D-057：准入主题随面板退役→毕业链裁决
}

// weeklySystemPrompt 周深挖约束（同日报红线 + 允许 query_ro 补查≤5 次）。
const weeklySystemPrompt = `你是 NOX 套利系统的探矿员（只读研究员），本次=周深挖。
主题由用户消息给出；用 query_ro 只读补查（≤5 次），数字只引用查询/输入原值不得自算。
输出=研究纪要 markdown 正文（## 观察 / ## 数据 / ## 提案线索 三节）。
红线：不预测价格；不给仓位建议；不跨场所借数据。
工作契约：每条结论性判断附证伪条件「若 <具体可观测值> 则此判断不成立」，说不出的降级为假设陈述；
「需观察」必须写明触发点（何时/何阈值/触发什么动作）；
引用 query_ro 结果附口径说明（时间窗/过滤/排序语义），警惕历史极值伪装现值、窗口函数截断；
两个数字/状态互斥时立即停推——把矛盾升为最高优先级调查对象，用 query_ro 复验两边来源，禁调和性解释或择一采用；
多个竞争假设并存时做判别性最小实验——先设计能区分它们的最小查询/数据切片（一次只动一个变量），不分别为每个假设找支持证据；
结论分层归因注明所属层（采集/存储/计算/展示），跨层断言须逐层给证据，禁跨层跳跃归因。`

// WeeklyResult 周深挖产出。
type WeeklyResult struct {
	Theme string
	Path  string // writeReport 落盘路径
}

// RunWeekly 执行周深挖。同日同主题幂等覆盖文件名。
func RunWeekly(ctx context.Context, deps Deps) (*WeeklyResult, error) {
	deps.fill()
	if deps.Store == nil || deps.Client == nil {
		return nil, fmt.Errorf("explorer: store/client 未配置")
	}
	now := deps.Now()
	_, week := now.In(BJTime).ISOWeek()
	theme := weeklyThemes[week%len(weeklyThemes)]

	// 主题上下文=当周离散度 top + 闲置池（确定性输入同构日扫①⑤）。
	disp, _ := deps.Store.ExplorerTermDispersion(ctx, now)
	idles, _ := deps.Store.ExplorerIdleFunds(ctx, now)
	var ctxBlock strings.Builder
	for i, d := range disp {
		if i >= 10 {
			break
		}
		fmt.Fprintf(&ctxBlock, "%s %s range=%.6g mean=%.6g\n", d.Kind, d.Canonical, d.Range, d.Mean)
	}
	for _, p := range idles {
		if p.Idle || p.BelowFloor {
			fmt.Fprintf(&ctxBlock, "idle %s %s bal=%.4g inc30=%.4g\n", p.Venue, p.VenueType, p.Balance, p.Income30dUSD)
		}
	}
	prompt := fmt.Sprintf("本周主题：%s。\n\n当周确定性快照：\n```\n%s```\n请产出研究纪要。",
		theme, ctxBlock.String())
	if !deps.Rate.Allow() {
		return nil, fmt.Errorf("explorer: rate limited")
	}
	var aiText strings.Builder
	_, err := deps.Run(ctx, deps.Client, deps.Tools, explorerToolSpecs(), weeklySystemPrompt,
		[]analyst.Message{analyst.TextContent("user", prompt)},
		analyst.LoopCallbacks{OnDelta: func(t string) { aiText.WriteString(t) }})
	if err != nil {
		return nil, fmt.Errorf("analyst run: %w", err)
	}
	day := ScanDate(now).Format("2006-01-02")
	name := fmt.Sprintf("EXPLORER-%s-%s-LLM.md", theme, day)
	body := fmt.Sprintf("# EXPLORER 周深挖 %s（%s）\n\n%s\n", theme, day, strings.TrimSpace(aiText.String()))
	path, err := writeReport(deps.ResearchDir, name, body)
	if err != nil {
		return nil, err
	}
	return &WeeklyResult{Theme: theme, Path: path}, nil
}

// explorerToolSpecs 只读白名单（query_ro 补查件——analyst.ToolExecutor 现成，nox_ro 拒写）。
func explorerToolSpecs() []analyst.ToolSpec {
	var out []analyst.ToolSpec
	for _, s := range analyst.ToolSpecs() {
		if s.Name == "query_ro" {
			out = append(out, s)
		}
	}
	return out
}
