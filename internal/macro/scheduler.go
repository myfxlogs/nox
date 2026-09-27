// scheduler.go —— D-528 S4：宏观采集调度（独立 goroutine，不接 collect.Scheduler；
// repo_reminder 先例）。三路独立（COT/财历/FRED），错一处不阻塞另两路。
// 节奏（BJ=Asia/Shanghai）：
//   - COT：周六 05:00 起 2h 间隔轮询，拿到新 report_date 或 12h 超时停；新报告落地即发中文周报
//   - 财历：每日 07:20 拉当周+下周窗口 upsert；每 4h 增量刷 actuals；每日 07:30 发中文日报
//   - FRED：每日 08:00 拉白名单序列
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
	UpsertCotPositions(ctx context.Context, rows []pgstore.CotPosition) (int, error)
	UpsertFredObs(ctx context.Context, rows []pgstore.FredObs) (int, error)
	MacroEventsBetween(ctx context.Context, from, to time.Time) ([]pgstore.MacroEvent, error)
	MaxCotReportDate(ctx context.Context) (time.Time, error)
}

// Notifier 推送面（*notify.FeishuClient 原生满足）。
type Notifier interface {
	SendText(ctx context.Context, chatID, text string) error
}

// Deps 调度依赖（Now 测试注入；ChatID=feishu_target 绑定推送目标）。
type Deps struct {
	Store   Store
	Notify  Notifier
	ChatID  string
	FredKey string
	Now     func() time.Time
}

// Run 阻塞至 ctx 取消（三路子 goroutine 经 safego 托管，panic 不拖垮 core）。
func Run(ctx context.Context, d Deps) {
	if d.Now == nil {
		d.Now = time.Now
	}
	client := &http.Client{Timeout: httpTimeout}
	safego.Go("macro:cot", func() { cotLoop(ctx, d, client) })
	safego.Go("macro:calendar", func() { calendarLoop(ctx, d, client) })
	safego.Go("macro:fred", func() { fredLoop(ctx, d, client) })
	log.Printf("[macro] scheduler started (cot sat 05:00BJ/2h · calendar 07:20BJ+4h · fred 08:00BJ)")
	<-ctx.Done()
}

// cotLoop 周六窗口轮询：05:00 BJ 起 2h 间隔，新 report_date 落地即发周报，12h 超时收窗。
func cotLoop(ctx context.Context, d Deps, client *http.Client) {
	for {
		next := nextWeeklyBJ(d.Now(), time.Saturday, 5, 0)
		if !sleepUntil(ctx, next) {
			return
		}
		baseline, err := d.Store.MaxCotReportDate(ctx)
		if err != nil {
			log.Printf("[macro:cot] baseline query: %v", err)
		}
		deadline := next.Add(12 * time.Hour)
		for {
			rows, err := FetchCot(ctx, client)
			if err != nil {
				log.Printf("[macro:cot] fetch: %v", err)
			} else if n, err := d.Store.UpsertCotPositions(ctx, rows); err != nil {
				log.Printf("[macro:cot] upsert: %v", err)
			} else {
				latest := maxReportDate(rows)
				log.Printf("[macro:cot] upserted %d rows, latest=%s", n, latest.Format("2006-01-02"))
				if latest.After(baseline) {
					announce(ctx, d, RenderCotCN(rows))
					break
				}
			}
			if !d.Now().Before(deadline) {
				log.Printf("[macro:cot] 12h window closed without new report_date")
				break
			}
			if !sleepUntil(ctx, d.Now().Add(2*time.Hour)) {
				return
			}
		}
	}
}

// calendarLoop 财历路：4h ticker 刷 actuals（自启动即跑）+ 每日 07:20 正式拉取 + 07:30 中文日报
// （单 goroutine select 双源，刷新 tick 不因日报窗口丢失节奏）。
func calendarLoop(ctx context.Context, d Deps, client *http.Client) {
	refresh := time.NewTicker(4 * time.Hour)
	defer refresh.Stop()
	fetchAndLog := func(why string) {
		if err := fetchCalendarWeeks(ctx, d, client); err != nil {
			log.Printf("[macro:calendar] %s: %v", why, err)
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
	announce(ctx, d, RenderCalendarCN(events, d.Now()))
}

// fredLoop 每日 08:00 BJ 拉白名单序列。
func fredLoop(ctx context.Context, d Deps, client *http.Client) {
	for {
		if !sleepUntil(ctx, nextDailyBJ(d.Now(), 8, 0)) {
			return
		}
		rows, err := FetchFred(ctx, client, d.FredKey, 10)
		if err != nil {
			log.Printf("[macro:fred] fetch: %v", err)
			continue
		}
		n, err := d.Store.UpsertFredObs(ctx, rows)
		if err != nil {
			log.Printf("[macro:fred] upsert: %v", err)
			continue
		}
		log.Printf("[macro:fred] upserted %d observations", n)
	}
}

// announce 周报/日报推送（失败 log 不重试——下一节奏自愈）。
func announce(ctx context.Context, d Deps, text string) {
	if d.Notify == nil || d.ChatID == "" {
		log.Printf("[macro] notify skipped (chat unbound): %s", firstLine(text))
		return
	}
	if err := d.Notify.SendText(ctx, d.ChatID, text); err != nil {
		log.Printf("[macro] send: %v", err)
	}
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

// maxReportDate 本批最新 report_date。
func maxReportDate(rows []pgstore.CotPosition) time.Time {
	var max time.Time
	for _, r := range rows {
		if r.ReportDate.After(max) {
			max = r.ReportDate
		}
	}
	return max
}

// sleepUntil ctx 感知睡眠（到点=true；取消=false）。
func sleepUntil(ctx context.Context, t time.Time) bool {
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

// nextWeeklyBJ 下一个 BJ 星期几 HH:MM。
func nextWeeklyBJ(now time.Time, weekday time.Weekday, hour, min int) time.Time {
	bj := now.In(bjLoc)
	t := time.Date(bj.Year(), bj.Month(), bj.Day(), hour, min, 0, 0, bjLoc)
	diff := (int(weekday) - int(bj.Weekday()) + 7) % 7
	t = t.AddDate(0, 0, diff)
	if !t.After(bj) {
		t = t.AddDate(0, 0, 7)
	}
	return t
}
