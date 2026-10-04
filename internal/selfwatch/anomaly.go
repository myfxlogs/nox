// anomaly.go —— D-033④：数据面异常告警（warn 级，1h 同键去重经 alerts 表实查——
// 进程重启不失忆）。只查 DB 不产数据；判定阈值全常量显式。
package selfwatch

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
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
	paperDataLost24h   = 50 // D-042 R1 ③：paper 双 CEX 轨 24h data_lost 退出行数 warn 阈
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
	checkPaperDataLost(ctx, d, now, warn)
	checkAnalystRO(ctx, d, warn)
	checkMacroBlackout(ctx, d, now, warn)
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
		warn("[自盯] 纸面计息停滞 %d 行（结算水位落后 >2 期）", ev.Stalled)
	}
	prev, _ := failClosedPrev(ctx, d)
	if ev.FailClosed > prev {
		warn("[自盯] 纸面计息缺数据行数环比突增 %d→%d（边界/率源/映射缺）", prev, ev.FailClosed)
	}
	if err := saveFailClosedPrev(ctx, d, ev.FailClosed); err != nil {
		log.Printf("[selfwatch] fail-closed prev save: %v", err)
	}
}

// checkPaperDataLost ⑥paper 双 CEX 轨 data_lost 退出行数（D-042 R1 ③：常态应近零，
// ≈4000/天=报价流死亡开平闭环失控；冷却中对数随行——同对 24h 内 ≥2 次 data_lost 停开）。
func checkPaperDataLost(ctx context.Context, d Deps, now time.Time, warn func(string, ...any)) {
	rows, pairs, err := d.Store.PaperDataLostStats(ctx, now.Add(-24*time.Hour))
	if err != nil {
		log.Printf("[selfwatch] paper data_lost stats: %v", err)
		return
	}
	if rows > paperDataLost24h {
		warn("[自盯] 纸面数据断供退出 24h %d 行（阈值 %d；冷却中 %d 对——24h 内不再开仓）",
			rows, paperDataLost24h, pairs)
	}
}

// checkMacroBlackout ⑦执行禁区拒开次数/24h（D-053 ④：>0 warn——禁区命中本身即业主
// 应知事件；1h dedup 节流，1h 内多单合并为一条累计文案由下轮刷新）。
func checkMacroBlackout(ctx context.Context, d Deps, now time.Time, warn func(string, ...any)) {
	n, err := d.Store.CountMacroBlackoutRejects(ctx, now.Add(-24*time.Hour))
	if err != nil {
		log.Printf("[selfwatch] macro blackout rejects: %v", err)
		return
	}
	if n > 0 {
		warn("[自盯] 财历禁区拒开 %d 次/24h（高影响事件窗内禁开仓）", n)
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
		warn("[自盯] 财历表空（ARB_MACRO_ENABLED=true 但零行）")
		return
	}
	if lag := now.Sub(ts); lag > macroCalendarStale {
		warn("[自盯] 财历拉取停滞 %.1fh（最新落库 %s）",
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
		warn("[自盯] 休市周表数据缺席 >%v（MT 休市闸无源——休市判定空转）", tradeSessionAbsent)
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
		warn("[自盯] 息差数据零行（滚动均值/经济闸无源）")
		return
	}
	if lag := now.Sub(ts); lag > fundingStale {
		warn("[自盯] 息差数据超龄 %.1fh（最新 %s）", lag.Hours(), ts.UTC().Format("01-02 15:04Z"))
	}
}

// checkAnalystRO analyst 只读链路探针（D-059 ②）：经热读 ARB_AI_PG_DSN SELECT 1，
// 失败→warn（本次断链靠 LLM 自觉写进报告才被发现——降级不得无声）。
// err 文本过 redactPassword：keyword= 与 URL scheme://user:pass@ 两形态均打码
// （R2 ②：pgx parse 错误透 DSN 原文，仅 keyword 打码不命中 URL 形态）。
func checkAnalystRO(ctx context.Context, d Deps, warn func(string, ...any)) {
	if d.ProbeAnalystRO == nil {
		return // 未接线（测试替身/装配缺省）跳过
	}
	if err := d.ProbeAnalystRO(ctx); err != nil {
		warn("[自盯] analyst 只读链路断（query_ro 不可用）：%s", redactPassword(err.Error()))
	}
}

// redactPassword 错误文本中凭据段打码：keyword 形态 password=<值> 与 URL 形态
// scheme://user:<pass>@（R2 ②补）均替换为 ***；两形态可并存重复应用。
func redactPassword(s string) string {
	s = redactKeywordPassword(s)
	return redactURLPassword(s)
}

func redactKeywordPassword(s string) string {
	i := strings.Index(s, "password=")
	if i < 0 {
		return s
	}
	j := i + len("password=")
	for j < len(s) && s[j] != ' ' && s[j] != '\'' {
		j++
	}
	return s[:i] + "password=***" + s[j:]
}

// redactURLPassword URL 形态：scheme://user:secret@ → scheme://user:***@
// （正则锚定 scheme:// 与 @ 之间的 user:secret 凭据段）。
var reURLCred = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/@\s:]+):([^@\s]+)@`)

func redactURLPassword(s string) string {
	return reURLCred.ReplaceAllString(s, "$1$2:***@")
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
