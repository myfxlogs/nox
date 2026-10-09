// scheduler.go —— D-528 S4：宏观财历调度（独立 goroutine，不接 collect.Scheduler；
// repo_reminder 先例）。D-120 清退 COT/FRED 两采集路，仅留财历+实况链。
// 节奏（BJ=Asia/Shanghai）：
//   - 财历：每日 07:20 拉当周+下周窗口 upsert；每 4h 增量刷 actuals；每日 07:30 发中文日报
package macro

import (
	"context"
	"log"
	"net/http"
	"time"

	"arb/internal/safego"
	"arb/internal/store/pgstore"
)

// httpTimeout 采集客户端超时（不复用 cfg.Client——那是 cex-proxy 通道）。
const httpTimeout = 30 * time.Second

// Store 宏观调度存储面（*pgstore.Store 原生满足）。
type Store interface {
	UpsertMacroEvents(ctx context.Context, events []pgstore.MacroEvent) (int, error)
	MacroEventsBetween(ctx context.Context, from, to time.Time) ([]pgstore.MacroEvent, error)
	NextHighEventAt(ctx context.Context, now time.Time) (time.Time, bool, error)                                  // D-053 ②
	MacroHighEventsIn(ctx context.Context, from, to time.Time, currencies []string) ([]pgstore.MacroEvent, error) // D-053 ②③
	InsertAlertDedup(ctx context.Context, level, message string, window time.Duration) error                      // D-053 ②
}

// Deps 调度依赖（Now 测试注入）。
// D-545 热读化：Enabled 改 getter——每班唤醒首判（ARB_MACRO_ENABLED 热闸：
// false=当班跳过不杀 goroutine；nil=恒开）。D-120 删 FredKey（FRED 面清退）。
// D-147：Notify/ChatID 摘除——推送收口业主域，财历/actuals 仅 alerts 留痕。
type Deps struct {
	Store   Store
	Enabled func() bool
	Now     func() time.Time
}

// enabled 当班闸（nil=恒开保持旧语义；false=当班跳过）。
func (d Deps) enabled(name string) bool {
	if d.Enabled != nil && !d.Enabled() {
		log.Printf("[macro:%s] disabled 当班跳过", name)
		return false
	}
	return true
}

// Run 阻塞至 ctx 取消（三路子 goroutine 经 safego 托管，panic 不拖垮 core）。
func Run(ctx context.Context, d Deps) {
	if d.Now == nil {
		d.Now = time.Now
	}
	client := &http.Client{Timeout: httpTimeout}
	rearm := make(chan struct{}, 1) // D-053 ②：calendar upsert 成功 → actualsLoop 重臂信号（仅唤醒不携数据）
	safego.GoLoop("macro:calendar", func() { calendarLoop(ctx, d, client, rearm) })
	safego.GoLoop("macro:actuals", func() { runActualsLoop(ctx, d, client, rearm) })
	log.Printf("[macro] scheduler started (calendar 07:20BJ+4h · actuals event-driven)")
	<-ctx.Done()
}

// calendarLoop 财历路：4h ticker 刷 actuals（自启动即跑；D-053 ②降为兜底——actualsLoop
// 事件驱动为主）+ 每日 07:20 正式拉取 + 07:30 中文日报（单 goroutine select 双源，刷新 tick
// 不因日报窗口丢失节奏）。每轮 upsert 成功后 non-blocking 通知 rearm（actualsLoop 重臂）。
func calendarLoop(ctx context.Context, d Deps, client *http.Client, rearm chan<- struct{}) {
	refresh := time.NewTicker(4 * time.Hour)
	defer refresh.Stop()
	fetchAndLog := func(why string) {
		if !d.enabled("calendar") {
			return
		}
		if err := fetchCalendarWeeks(ctx, d, client); err != nil {
			log.Printf("[macro:calendar] %s: %v", why, err)
			return
		}
		select { // D-053 ②：upsert 成功 → actualsLoop 重臂（满则丢——loop 下轮自会重扫）
		case rearm <- struct{}{}:
		default:
		}
	}
	fetchAndLog("boot-refresh")
	for {
		daily := time.NewTimer(time.Until(nextDailyBJ(d.Now(), 7, 20)))
	wait:
		for {
			select {
			case <-ctx.Done():
				daily.Stop()
				return
			case <-refresh.C:
				fetchAndLog("4h-refresh")
			case <-daily.C:
				fetchAndLog("daily-0720")
				break wait
			}
		}
		if !sleepUntil(ctx, nextDailyBJ(d.Now(), 7, 30)) {
			return
		}
		if !d.enabled("calendar") {
			continue
		}
		sendDailyReport(ctx, d)
	}
}

// fetchCalendarWeeks 拉当周+下周窗口（BJ 周一 00:00 起 14 天）财历事件。
func fetchCalendarWeeks(ctx context.Context, d Deps, client *http.Client) error {
	now := d.Now().In(bjLoc)
	monday := now.AddDate(0, 0, -int((now.Weekday()+6)%7))
	from := time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, bjLoc)
	to := from.AddDate(0, 0, 14).Add(-time.Second)
	events, err := FetchMQL5Calendar(ctx, client, mql5URL, from, to)
	if err != nil {
		return err
	}
	n, err := d.Store.UpsertMacroEvents(ctx, events)
	if err != nil {
		return err
	}
	log.Printf("[macro:calendar] upserted %d events (%s ~ %s)", n, from.Format("01-02"), to.Format("01-02"))
	return nil
}

// sendDailyReport 当日+次日高/中影响事件中文日报（读库=已落数据真值）。
func sendDailyReport(ctx context.Context, d Deps) {
	now := d.Now().In(bjLoc)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, bjLoc)
	events, err := d.Store.MacroEventsBetween(ctx, dayStart, dayStart.AddDate(0, 0, 2))
	if err != nil {
		log.Printf("[macro:calendar] daily report query: %v", err)
		return
	}
	// D-147 推送收口：财历日报=系统风控输入非业主事件——info 留痕不直推（同文案 4h 防重）。
	if err := d.Store.InsertAlertDedup(ctx, "info", RenderCalendarCN(events, d.Now()), 4*time.Hour); err != nil {
		log.Printf("[macro:calendar] daily report alert: %v", err)
	}
}

// sleepUntil ctx 感知睡眠（到点=true；取消=false）。var=测试注入点（同 fredURL 先例：
// D-545 当班闸测假睡眠速过当班，生产零改动）。
var sleepUntil = func(ctx context.Context, t time.Time) bool {
	d := time.Until(t)
	if d < 0 {
		d = 0
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// nextDailyBJ 下一个 BJ HH:MM（含今日，若未过点）。
func nextDailyBJ(now time.Time, hour, min int) time.Time {
	bj := now.In(bjLoc)
	t := time.Date(bj.Year(), bj.Month(), bj.Day(), hour, min, 0, 0, bjLoc)
	if !t.After(bj) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}
