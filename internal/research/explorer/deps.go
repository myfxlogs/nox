// Package explorer D-513 探矿员壳（设计稿 v1.2）：日扫五项（确定性查询+一次 LLM 叙事）→
// explorer_proposals 记分板 + 待归纳收件箱行；周深挖 → docs/研究/ 幂等落盘；默认关（env 闸）。
// 纪律继承 sysretro 同款：数字全 Go 确定性算，LLM 只写叙事与提案；提案只进收件箱，
// 不自动立项/提交/执行；status 流转=席位手动回填；LLM 无 git 权。
package explorer

import (
	"context"
	"encoding/json"
	"time"

	"arb/internal/platform/analyst"
	"arb/internal/store/pgstore"
)

// BJTime 北京时区（FixedZone 同 sysretro——禁 LoadLocation，alpine 无 tzdata，D-510 教训）。
var BJTime = time.FixedZone("Asia/Shanghai", 8*3600)

// 默认路径（容器内 /kn 映射宿主仓库根；Deps 字段可覆盖便于测试指 tmpdir）。
const (
	DefaultInboxPath   = "/kn/docs/经验库/待归纳-分析员.md"
	DefaultResearchDir = "/kn/docs/研究"
)

// Store 探矿员数据面（pgstore.Store 实现；测试注入 fake）。
type Store interface {
	ExplorerTermDispersion(ctx context.Context, now time.Time) ([]pgstore.ExplorerDispersion, error)
	ExplorerIdleFunds(ctx context.Context, now time.Time) ([]pgstore.ExplorerIdlePool, error)
	ExplorerStateGet(ctx context.Context, key string) (json.RawMessage, bool, error)
	ExplorerStatePut(ctx context.Context, key string, val json.RawMessage) error
	ExplorerStatePutIfAbsent(ctx context.Context, key string, val json.RawMessage) (bool, error)
	ExplorerProposalInsert(ctx context.Context, ps []pgstore.ExplorerProposal) (int, error)
	ExplorerProbeProposalInsert(ctx context.Context, c pgstore.ProbeContract, scanDate time.Time) (int64, bool, error)
	ExplorerProbeProposalByPosition(ctx context.Context, posID int64) (*pgstore.ProbeProposal, error)
	AppConfigFloat(ctx context.Context, key string) (float64, bool, error)
	GetProbe(ctx context.Context, posID int64) (*pgstore.ProbeRow, error)
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	ListCashflows(ctx context.Context, posID int64) ([]pgstore.Cashflow, error)
	LoadRadarWatchlist(ctx context.Context) ([]pgstore.RadarWatchlistRow, error)
	ListSymbolMappings(ctx context.Context, venue string) ([]pgstore.SymbolMapping, error)
	ListAlerts(ctx context.Context, limit, offset int) ([]pgstore.Alert, error)
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// Runner RunWithSpecs 等价 seam（同 sysretro.Runner，测试注入 fake 断言 prompt/输出）。
type Runner func(ctx context.Context, c *analyst.Client, tools analyst.ToolExecutor,
	specs []analyst.ToolSpec, systemPrompt string, msgs []analyst.Message,
	cb analyst.LoopCallbacks) (analyst.LoopResult, error)

// Deps 探矿员依赖（wire_explorer 装配；测试注入）。
type Deps struct {
	Store  Store
	Client *analyst.Client
	Tools  analyst.ToolExecutor
	Run    Runner           // 空=analyst.RunWithSpecs
	Now    func() time.Time // 空=time.Now
	Rate   *analyst.RateLimiter
	// ResolveClient wire 注入（kbreport.ResolveClient 口径，每次触发重解析 DB 优先）；
	// nil=直接用静态 Client 字段。
	ResolveClient func(ctx context.Context) (*analyst.Client, error)
	// ProbeKick D-163：探针提案落库后唤醒机审（cmd/core 接 bus.Topic 非阻塞发布；nil=不踢）。
	ProbeKick   func()
	InboxPath   string // 空=DefaultInboxPath
	ResearchDir string // 空=DefaultResearchDir
}

func (d *Deps) fill() {
	if d.InboxPath == "" {
		d.InboxPath = DefaultInboxPath
	}
	if d.ResearchDir == "" {
		d.ResearchDir = DefaultResearchDir
	}
	if d.Run == nil {
		// D-098 S1/S2 缺省 Runner：explorer 三面（daily/weekly/probe）调用预算 300s
		//（大 prompt+8192 预算的合法调用截止，ctx 到点自然截止）+ thinking disabled
		//（结构化 JSON 输出面，思考纯耗限时——探针实证 512 预算全耗隐形推理 chars=0）。
		d.Run = func(ctx context.Context, c *analyst.Client, tools analyst.ToolExecutor,
			specs []analyst.ToolSpec, systemPrompt string, msgs []analyst.Message,
			cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
			ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
			defer cancel()
			return analyst.RunWithSpecsMaxTokensThinking(ctx, c, tools, specs, systemPrompt, msgs, cb, 8192, json.RawMessage(`{"type":"disabled"}`))
		}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Rate == nil {
		d.Rate = analyst.NewRateLimiter(10) // ARB_EXPLORER_RATE_PER_HOUR 缺省 10，独立桶不占前端 analyst 配额
	}
}

// alert best-effort 通知（store nil/插入失败降级为调用方 log——同 sysretro 口径）。
func (d *Deps) alert(ctx context.Context, level, msg string) {
	if d.Store == nil {
		return
	}
	_ = d.Store.InsertAlert(ctx, nil, level, msg)
}
