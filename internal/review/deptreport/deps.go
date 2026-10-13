// Package deptreport 公司日报（D-246 v3 §4.4 / D-247）：日频（07:05 BJ）1 次
// analyst 调用——PG 确定性指标查询 → money-first 固定序输入段 → LLM 纯综合
// （数值只取查询结果禁编数）→ fin_dept_reports dept='company' 落库 + 飞书扇出。
// 禁线：报告不进入风控/执行/淘汰决策路径（LLM 输出仅观察+建议面）；
// 全程 fail-open——调用失败只 log，不阻塞业务；重启不补班。
package deptreport

import (
	"context"
	"encoding/json"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/store/pgstore"
)

// BJTime 北京时区（FixedZone 同 explorer/sysretro——禁 LoadLocation，alpine 无 tzdata）。
var BJTime = time.FixedZone("Asia/Shanghai", 8*3600)

// DeptCompany 公司日报行 dept 键（fin_dept_reports.dept；v3=单日报唯一行）。
const DeptCompany = "company"

// Store 报告数据面（*pgstore.Store 原生满足；测试注入 fake）。
type Store interface {
	RetroDailyStats(ctx context.Context, start, end time.Time) (*pgstore.RetroDaily, error)
	ExplorerIdleFunds(ctx context.Context, now time.Time) ([]pgstore.ExplorerIdlePool, error)
	ComputeNav(ctx context.Context, now time.Time) (*pgstore.NavReport, error) // D-264 M4.4②：NAV 单函数
	NetRealizedSince(ctx context.Context, since time.Time, est bool) (float64, error)
	ListProposals(ctx context.Context, status string, limit int) ([]pgstore.Proposal, error)
	ListConditionGaps(ctx context.Context, status string) ([]pgstore.ConditionGap, error)
	ListDisabledStrategies(ctx context.Context) (map[string]bool, error)
	LLMCallCostSince(ctx context.Context, since time.Time) (float64, int, error)
	UpsertDeptReport(ctx context.Context, r pgstore.DeptReport) error
	InsertAlertDedup(ctx context.Context, level, message string, window time.Duration) error
}

// Notifier 飞书推送面（*notify.FeishuClient 原生满足；nil=不推）。
type Notifier interface {
	Enabled() bool
	SendText(ctx context.Context, chatID, text string) (string, error)
}

// Runner RunWithSpecs 等价 seam（同 explorer/sysretro，测试注入 fake 断言 prompt/输出）。
type Runner func(ctx context.Context, c *analyst.Client, tools analyst.ToolExecutor,
	specs []analyst.ToolSpec, systemPrompt string, msgs []analyst.Message,
	cb analyst.LoopCallbacks) (analyst.LoopResult, error)

// Deps 日报依赖（wire_deptreport 装配；测试注入）。
type Deps struct {
	Store  Store
	Client *analyst.Client
	Tools  analyst.ToolExecutor // nil=日报不带工具（数字全输入段给，LLM 无查询面）
	Run    Runner               // 空=analyst.RunWithSpecs 包装（180s 预算+思考关+4096）
	Now    func() time.Time     // 空=time.Now
	Rate   *analyst.RateLimiter // nil=不限流；wire 复用 aiRate 桶（1 调用/天）
	// CapitalBaseUSD 资金基数（§4.3 指标①30d 净额对 −3% 硬线+指标⑤年化分母）。
	CapitalBaseUSD float64
	// ResolveClient wire 注入（kbreport.ResolveClient 口径，每班重解析 DB 优先）；
	// nil=直接用静态 Client 字段。AI 未配置=当班整体跳过（fail-open）。
	ResolveClient func(ctx context.Context) (*analyst.Client, error)
	Notify        Notifier                                  // nil=不推（只落库+alert 留痕）
	ChatID        func(ctx context.Context) (string, error) // nil=不推
	Enabled       func() bool                               // nil=恒开；false=当班跳过
	// Beat 每班心跳钩（wire 注入 safego.Beat 保台账节拍新鲜——Run fn 内睡整天，
	// 仅 GoLoop 启动拍一拍的话 cadence=86400 超龄即滞跳假阳性；nil=不拍）。
	Beat func()
	// OnReport 可选观察钩（测试/未来看板推送点；行落库后回调，nil=跳过）。
	OnReport func(r pgstore.DeptReport)
	// OnDayCut M4.4①：日切钩——Run 每班触发点+boot 首切各调一次
	// （wire 注 budget.DailyCut：fin_nav_daily 落行+fin_strategy_budget 重算；
	// nil=不切）。确定性零 LLM，在 Enabled 闸前跑（日报关≠日切关）。
	OnDayCut func(ctx context.Context, dayStart time.Time) error
}

func (d *Deps) fill() {
	if d.Run == nil {
		// 公司日报=结构化短输出面：180s 预算 + thinking disabled + 4096 上限
		//（同 explorer D-098 口径；1 调用/天）。
		d.Run = func(ctx context.Context, c *analyst.Client, tools analyst.ToolExecutor,
			specs []analyst.ToolSpec, systemPrompt string, msgs []analyst.Message,
			cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
			ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
			defer cancel()
			return analyst.RunWithSpecsMaxTokensThinking(ctx, c, tools, specs, systemPrompt, msgs, cb, 4096, json.RawMessage(`{"type":"disabled"}`))
		}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
}

// resolveAI 每班重解析 client（DB 优先 env 回落）；未配置=nil,nil（当班跳过非错误）。
func (d *Deps) resolveAI(ctx context.Context) (*analyst.Client, error) {
	if d.ResolveClient != nil {
		return d.ResolveClient(ctx)
	}
	if d.Client == nil || d.Client.APIKey == "" {
		return nil, nil
	}
	return d.Client, nil
}
