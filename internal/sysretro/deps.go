// Package sysretro 系统周期复盘报告（D-457）：日结 00:30 UTC + 周一 01:00 UTC 复盘。
// 纪律（SSOT §4）：数字全 Go 确定性算（pgstore.RetroDaily/RetroWeekly），AI 只写叙事与建议；
// 建议只进 docs/经验库/待归纳-分析员.md 收件箱分诊，不自动立项/执行。
package sysretro

import (
	"context"
	"time"

	"arb/internal/analyst"
	"arb/internal/store/pgstore"
)

// 默认路径（容器内 /kn 映射宿主仓库根；Deps 字段可覆盖便于测试指 tmpdir）。
const (
	DefaultReportDir  = "/kn/docs/报告"
	DefaultInboxPath  = "/kn/docs/经验库/待归纳-分析员.md"
	DefaultStatePath  = "/kn/docs/handoff/STATE.md"
	DefaultRepoDir    = "/kn"
	DefaultEntriesDir = "/kn/docs/经验库/条目"
)

// Store 报告数据面（pgstore.Store 实现；测试注入 fake）。
type Store interface {
	RetroDailyStats(ctx context.Context, start, end time.Time) (*pgstore.RetroDaily, error)
	RetroWeeklyStats(ctx context.Context, since, until time.Time) (*pgstore.RetroWeekly, error)
	// DepthFacts D-578：cex_depth_sampler 窗内采样行（三所深度章节）。
	DepthFacts(ctx context.Context, since, until time.Time) ([]pgstore.DepthFact, error)
	// AppConfigFloat D-180：统计证据阈值热读（Zσ/转折点标注线，种子径 app_config）。
	AppConfigFloat(ctx context.Context, key string) (float64, bool, error)
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// Runner RunWithSpecs 等价 seam（同 kbreport.Runner，测试注入 fake 断言 prompt/输出）。
type Runner func(ctx context.Context, c *analyst.Client, tools analyst.ToolExecutor,
	specs []analyst.ToolSpec, systemPrompt string, msgs []analyst.Message,
	cb analyst.LoopCallbacks) (analyst.LoopResult, error)

// Deps Generate 依赖（wire_sysretro / dashboard RPC 装配）。
type Deps struct {
	Store      Store
	Client     *analyst.Client
	Tools      analyst.ToolExecutor
	Run        Runner           // 空=analyst.RunWithSpecs
	Now        func() time.Time // 空=time.Now
	ReportDir  string           // 空=DefaultReportDir
	InboxPath  string           // 空=DefaultInboxPath
	StatePath  string           // 空=DefaultStatePath
	RepoDir    string           // 空=DefaultRepoDir（git log 交接面用，无 git 则降级）
	EntriesDir string           // 空=DefaultEntriesDir（周报 §4 KB 增量）
}

func (d *Deps) fill() {
	if d.ReportDir == "" {
		d.ReportDir = DefaultReportDir
	}
	if d.InboxPath == "" {
		d.InboxPath = DefaultInboxPath
	}
	if d.StatePath == "" {
		d.StatePath = DefaultStatePath
	}
	if d.RepoDir == "" {
		d.RepoDir = DefaultRepoDir
	}
	if d.EntriesDir == "" {
		d.EntriesDir = DefaultEntriesDir
	}
	if d.Run == nil {
		d.Run = analyst.RunWithSpecs
	}
	if d.Now == nil {
		d.Now = time.Now
	}
}

// alert best-effort 通知（store nil / 插入失败均降级为 log 由调用方处理——报告本体已落盘为重）。
func (d *Deps) alert(ctx context.Context, level, msg string) {
	if d.Store == nil {
		return
	}
	_ = d.Store.InsertAlert(ctx, nil, level, msg)
}
