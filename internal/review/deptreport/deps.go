// Package deptreport 部门日报→经理摘要（D-244，业主令）：日频（07:05 BJ）逐十一部
// 跑 analyst——PG 确定性指标查询 → 结构化部报（数值只取查询结果，LLM 纯综合禁编数）；
// 账本部专项财务日报（income/positions/accounts/NAV/现金，D-013 单源账本口径）；
// 十一部报+财报合成经理摘要 → fin_dept_reports 落库 + 飞书扇出 + 看板 RPC 读面。
// 禁线：报告不进入风控/执行/淘汰决策路径（LLM 输出仅观察+建议面）；
// 全程 fail-open——任一部报失败只 log，不阻塞其余部门与业务。
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

// DeptManager 经理摘要行 dept 键（fin_dept_reports.dept）。
const DeptManager = "manager"

// Store 报告数据面（*pgstore.Store 原生满足；测试注入 fake）。
type Store interface {
	ListEmployees(ctx context.Context) ([]pgstore.Employee, error)
	RetroDailyStats(ctx context.Context, start, end time.Time) (*pgstore.RetroDaily, error)
	FinancePnlByEnv(ctx context.Context) ([]pgstore.FinancePnlEnv, error)
	FinanceIncomeByKind(ctx context.Context) ([]pgstore.FinanceIncomeKind, error)
	LatestVenueAccountSnapshots(ctx context.Context) ([]pgstore.VenueAccountLatest, error)
	UpsertDeptReport(ctx context.Context, r pgstore.DeptReport) error
	ListDeptReports(ctx context.Context, date time.Time) ([]pgstore.DeptReport, error)
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
	Tools  analyst.ToolExecutor // nil=部报不带工具（数字全输入段给，LLM 无查询面）
	Run    Runner               // 空=analyst.RunWithSpecs 包装（180s 预算+思考关+4096）
	Now    func() time.Time     // 空=time.Now
	Rate   *analyst.RateLimiter // nil=不限流；wire 复用 aiRate 桶（~12 调用/天）
	// ResolveClient wire 注入（kbreport.ResolveClient 口径，每班重解析 DB 优先）；
	// nil=直接用静态 Client 字段。AI 未配置=当班整体跳过（fail-open）。
	ResolveClient func(ctx context.Context) (*analyst.Client, error)
	Notify        Notifier                                  // nil=不推（只落库+alert 留痕）
	ChatID        func(ctx context.Context) (string, error) // nil=不推
	Enabled       func() bool                               // nil=恒开；false=当班跳过
	// OnReport 可选观察钩（测试/未来看板推送点；每行落库后回调，nil=跳过）。
	OnReport func(r pgstore.DeptReport)
}

func (d *Deps) fill() {
	if d.Run == nil {
		// 部报/摘要=结构化短输出面：180s 预算 + thinking disabled + 4096 上限
		//（同 explorer D-098 口径；单次调用 ~12/天成本可忽略）。
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
