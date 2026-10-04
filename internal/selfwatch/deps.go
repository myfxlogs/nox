// deps.go —— D-033 系统自盯汇报面：依赖面（DB 单源读+告警写+推送+落档）。
// 调度=看门狗形态：timer 只定点唤醒，数据全在触发时刻查 DB，无任何周期刷新/缓存。
package selfwatch

import (
	"context"
	"time"

	"arb/internal/scan"
	"arb/internal/store/pgstore"
	"arb/internal/sysretro"
)

// cfgPaperWindowStart paper 观测窗起点键（迁移 0173 播种=D-028 部署时刻；env 兜底后
// 两者皆缺 → 报告显式「未配置」fail-closed，不误报窗龄/净额）。
const cfgPaperWindowStart = "ARB_PAPER_WINDOW_START"

// cfgPaperNetHist 逐日净额快照状态键（accrued_usd 库内为累计值，逐日净额=逐日快照
// 差分——状态存 app_config 跨进程重启存活，同 paper_window_report.sh state 目录语义）。
const cfgPaperNetHist = "ARB_SELFWATCH_PAPER_NET"

// cfgFailClosedPrev fail-closed 行数环比基数（上次观测值+时点，JSON）。
const cfgFailClosedPrev = "ARB_SELFWATCH_FAILCLOSED_PREV"

// cfgStage1Ready Stage 1 就绪态标记（"1"/"0"——就绪推送只在 ❌→✅ 转换时一次，
// 态存 app_config 跨进程重启存活；就绪态本身仍每日附在日报条件表）。
const cfgStage1Ready = "ARB_SELFWATCH_STAGE1_READY"

// Store 自盯存储窄面（*pgstore.Store 原生实现）。
type Store interface {
	AppConfigString(ctx context.Context, key string) (string, bool, error)
	AppConfigInt64(ctx context.Context, key string) (int64, bool, error)
	UpsertAppConfig(ctx context.Context, key, value, note string) error
	PaperWindowCohort(ctx context.Context, structure string, since time.Time) ([]pgstore.PaperSpotCarry, error)
	IncidentCountSince(ctx context.Context, since time.Time) (int, error)
	IncidentBlockerID(ctx context.Context, ackID int64) (int64, error)
	NetRealizedSince(ctx context.Context, since time.Time, excludeIncidents bool) (float64, error)
	SchemaMigrationMax(ctx context.Context) (int, error)
	ExposureCapCounts(ctx context.Context) (venueCaps, canonicalCaps int, err error)
	XAssetAllowRealPairs(ctx context.Context) ([]pgstore.VenuePair, error)
	LatestFactTs(ctx context.Context, kind string) (time.Time, bool, error)
	CountFactsSince(ctx context.Context, kind string, since time.Time) (int, error)
	MacroEventMaxCreated(ctx context.Context) (time.Time, bool, error)
	MakerLegStatsSince(ctx context.Context, since time.Time) (opens, incidents int, err error)
	PaperDataLostStats(ctx context.Context, since time.Time) (rows, cooldownPairs int, err error)
	PaperActiveStats(ctx context.Context) (pgstore.PaperActiveSplit, error)
	CountMacroBlackoutRejects(ctx context.Context, since time.Time) (int, error) // D-053 ④
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	InsertAlertDedup(ctx context.Context, level, message string, window time.Duration) error
}

// Notifier 推送面（*notify.FeishuClient 原生满足；nil=不推送）。
type Notifier interface {
	Enabled() bool
	SendText(ctx context.Context, chatID, text string) error
}

// Deps 自盯调度依赖。
// EvalPaper=scan.EvalPaperCohort 绑店（注入便于测试替身）；Weekly=autotrade.BuildRiskReport
// 绑店（D-032 ④ 周报文案单点）；KillActive=ControlPlane.IsKillSwitchActive；
// ChatID=st.FeishuChatID。ReportDir 空=sysretro.DefaultReportDir。
type Deps struct {
	Store       Store
	EvalPaper   func(ctx context.Context, rows []pgstore.PaperSpotCarry, since, now time.Time) (*scan.PaperWindowEval, error)
	Weekly      func(ctx context.Context, now time.Time) (string, error)
	KillActive  func(ctx context.Context) (bool, error)
	CapitalBase float64
	Notify      Notifier
	ChatID      func(ctx context.Context) (string, error)
	ReportDir   string
	Now         func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) reportDir() string {
	if d.ReportDir != "" {
		return d.ReportDir
	}
	return sysretro.DefaultReportDir
}

// bj 北京时区（调度定点+报表日分桶口径）。
var bj = func() *time.Location {
	l, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return l
}()

// NextDailyBJ now 之后第一个 BJ 每日 h:m 定点（cron 类唤醒计算；测试锚）。
func NextDailyBJ(now time.Time, h, m int) time.Time {
	b := now.In(bj)
	next := time.Date(b.Year(), b.Month(), b.Day(), h, m, 0, 0, bj)
	for !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// sleepUntil timer 睡到 t（ctx 取消→false）。看门狗唯一合法形态：定点唤醒一次后
// 全量重算 DB 数据，ticker 不携带任何业务状态。
func sleepUntil(ctx context.Context, t time.Time) bool {
	tm := time.NewTimer(time.Until(t))
	defer tm.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-tm.C:
		return true
	}
}
