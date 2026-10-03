// anomaly.go —— D-033④：数据面异常告警（warn 级，1h 同键去重经 alerts 表实查——
// 进程重启不失忆）。只查 DB 不产数据；判定阈值全常量显式。
package selfwatch

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"arb/internal/fact"
)

// anomalyDedupWindow 同键告警去重窗（票面=1h）。
const anomalyDedupWindow = time.Hour

// 数据面阈值（常量显式——禁隐式域值）：
// tradeSessionAbsent 周表日刷缺席窗（D-029 R3：adapter attach 后每日一拉 + Kick）；
// macroCalendarStale 财历落库停滞窗（每日 07:20 + 4h 增量刷新，>48h=拉取确死）；
// fundingStale funding facts 流龄上限（mark-price WS 持续推送）。
const (
	tradeSessionAbsent = 26 * time.Hour
	macroCalendarStale = 48 * time.Hour
	fundingStale       = 4 * time.Hour
)

// CheckAnomalies 单轮数据面体检（每小时唤醒调用一次；全部查询在唤醒时刻发生）。
func CheckAnomalies(ctx context.Context, d Deps) {
	now := d.now()
	warn := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		if err := d.Store.InsertAlertDedup(ctx, "warn", msg, anomalyDedupWindow); err != nil {
			log.Printf("[selfwatch] anomaly alert insert: %v", err)
		}
	}
	checkPaperHealth(ctx, d, now, warn)
	checkMacroCalendar(ctx, d, now, warn)
	checkTradeSession(ctx, d, now, warn)
	checkFundingFresh(ctx, d, now, warn)
}

// checkPaperHealth ①停滞行>0 + ②fail-closed 环比突增（基数落 app_config 存活重启）。
func checkPaperHealth(ctx context.Context, d Deps, now time.Time, warn func(string, ...any)) {
	since, ok, err := paperWindowStart(ctx, d)
	if err != nil {
		log.Printf("[selfwatch] anomaly window start: %v", err)
		return
	}
	if !ok {
		return // 窗未配置 → 日报已显式报缺，异常面不重复告警
	}
	rows, err := d.Store.PaperWindowCohort(ctx, paperWindowStructure, since)
	if err != nil {
		log.Printf("[selfwatch] anomaly cohort: %v", err)
		return
	}
	ev, err := d.EvalPaper(ctx, rows, since, now)
	if err != nil {
		log.Printf("[selfwatch] anomaly eval: %v", err)
		return
	}
	if ev.Stalled > 0 {
		warn("[自盯] paper accrue 停滞 %d 行（last_accrue_ts 落后 >2 结算窗）", ev.Stalled)
	}
	prev, _ := failClosedPrev(ctx, d)
	if ev.FailClosed > prev {
		warn("[自盯] paper fail-closed 行数环比突增 %d→%d（边界/率源/映射缺）", prev, ev.FailClosed)
	}
	if err := saveFailClosedPrev(ctx, d, ev.FailClosed); err != nil {
		log.Printf("[selfwatch] fail-closed prev save: %v", err)
	}
}

// checkMacroCalendar ③财历拉取失败（ARB_MACRO_ENABLED=false=显式停用 → 不查）；
// 信号=macro_event.created_at 最新时点（成功拉取才落新行）。
func checkMacroCalendar(ctx context.Context, d Deps, now time.Time, warn func(string, ...any)) {
	v, ok, err := d.Store.AppConfigString(ctx, "ARB_MACRO_ENABLED")
	if err != nil {
		log.Printf("[selfwatch] macro enabled read: %v", err)
		return
	}
	if !ok {
		v = os.Getenv("ARB_MACRO_ENABLED")
	}
	if v != "true" {
		return
	}
	ts, has, err := d.Store.MacroEventMaxCreated(ctx)
	if err != nil {
		log.Printf("[selfwatch] macro max created: %v", err)
		return
	}
	if !has {
		warn("[自盯] macro 财历表空（ARB_MACRO_ENABLED=true 但 macro_event 零行）")
		return
	}
	if lag := now.Sub(ts); lag > macroCalendarStale {
		warn("[自盯] macro 财历拉取停滞 %.1fh（macro_event 最新落库 %s）",
			lag.Hours(), ts.UTC().Format("01-02 15:04Z"))
	}
}

// checkTradeSession ④trade_session 周表缺席（D-029：MT 休市判定主信号——
// 缺席则休市闸空转）。
func checkTradeSession(ctx context.Context, d Deps, now time.Time, warn func(string, ...any)) {
	n, err := d.Store.CountFactsSince(ctx, fact.KindTradeSession, now.Add(-tradeSessionAbsent))
	if err != nil {
		log.Printf("[selfwatch] trade_session count: %v", err)
		return
	}
	if n == 0 {
		warn("[自盯] trade_session facts 缺席 >%v（MT 休市闸无源——休市判定空转）", tradeSessionAbsent)
	}
}

// checkFundingFresh ⑤funding facts 超龄（D-027 滚动均值/carry 判定的输入流）。
func checkFundingFresh(ctx context.Context, d Deps, now time.Time, warn func(string, ...any)) {
	ts, ok, err := d.Store.LatestFactTs(ctx, fact.KindFunding)
	if err != nil {
		log.Printf("[selfwatch] funding fact ts: %v", err)
		return
	}
	if !ok {
		warn("[自盯] funding facts 零行（carry 均值/经济闸无源）")
		return
	}
	if lag := now.Sub(ts); lag > fundingStale {
		warn("[自盯] funding facts 超龄 %.1fh（最新 %s）", lag.Hours(), ts.UTC().Format("01-02 15:04Z"))
	}
}

// failClosedPrev 上次观测的 fail-closed 行数（app_config 持久基数）。
func failClosedPrev(ctx context.Context, d Deps) (int, bool) {
	v, ok, err := d.Store.AppConfigString(ctx, cfgFailClosedPrev)
	if err != nil || !ok {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

func saveFailClosedPrev(ctx context.Context, d Deps, n int) error {
	return d.Store.UpsertAppConfig(ctx, cfgFailClosedPrev, fmt.Sprint(n), "D-033 fail-closed 环比基数（内部状态键）")
}
