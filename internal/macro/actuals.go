// actuals.go —— D-053 ②：实际值事件驱动推送。以库中最早未来 high 事件臂定时器
// （仅唤醒不携数据，同 D-047 NextProbeExpiryAt 形态；无事件=不臂，upsert 后经 rearm 重臂），
// 到点 +3min 拉当日窗口 upsert（actual 出值通常在发布后数分钟内）；actual 空→非空的
// high 行推飞书「<BJ时刻> <币种> <事件>：实际 X（预期 Y / 前值 Z）」+ alerts dedup 防重推。
// 4h ticker 兜底（calendarLoop refresh）保留——actual 迟到行经 rearm 触发本 loop 重扫补推。
package macro

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"arb/internal/store/pgstore"
)

// actualsSeenInitialFresh 首扫/重启后「historical actual 静默窗」：actual 已非空但事件
// 时点在该窗内 → 视为刚发生照推（否则静默记值防历史重推）。重启边界残推由 alerts
// dedup（actualPushDedup）兜底。
const actualsSeenInitialFresh = 30 * time.Minute

// actualPushDedup 同文案推送去重窗（alerts 表；跨重启防重推第二道）。
const actualPushDedup = 4 * time.Hour

// actualsWakeDelay 事件时点后延迟拉取（actual 出值非瞬时）。
const actualsWakeDelay = 3 * time.Minute

// runActualsLoop 阻塞至 ctx 取消。rearm：calendarLoop 每轮 upsert 成功后发一次
// （non-blocking）——新事件落库/actual 刷新 → 重臂+重扫。d.Now 注入（测试速过）。
func runActualsLoop(ctx context.Context, d Deps, client *http.Client, rearm <-chan struct{}) {
	seen := map[int64]string{} // event id → 上次见到的 actual 值（""=未见出值）
	seeded := false
	for {
		now := d.Now()
		t0, ok, err := d.Store.NextHighEventAt(ctx, now)
		if err != nil {
			log.Printf("[macro:actuals] next high event: %v", err)
			if !sleepUntil(ctx, now.Add(5*time.Minute)) {
				return
			}
			continue
		}
		if ok && !seeded {
			seedSeen(ctx, d, seen, now)
			seeded = true
		}
		if !ok {
			// 无未来 high 事件：不臂，等 upsert 信号（D-047 形态：定时器仅唤醒不携数据）。
			select {
			case <-ctx.Done():
				return
			case <-rearm:
			}
			continue
		}
		wake := t0.Add(actualsWakeDelay)
		log.Printf("[macro:actuals] armed for %s (event %s)", wake.Format("01-02 15:04Z"), t0.Format("01-02 15:04Z"))
		timer := time.NewTimer(time.Until(wake))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-rearm: // upsert 后重臂（可能出现了更早事件/actual 已刷新）
			timer.Stop()
			continue
		case <-timer.C:
		}
		if !d.enabled("actuals") {
			continue
		}
		// 到点：拉当日窗口 upsert（actual 刷新进库），随后重扫推送。
		if err := fetchCalendarDay(ctx, d, client, now); err != nil {
			log.Printf("[macro:actuals] day fetch: %v", err)
		}
		sweepActuals(ctx, d, seen, d.Now())
	}
}

// seedSeen 首扫播种：actual 已非空的事件记值不推（防重启/上线重推历史）；
// 仅 initialFresh 窗内的视为刚发生照推（actualsSeenInitialFresh 注释）。
func seedSeen(ctx context.Context, d Deps, seen map[int64]string, now time.Time) {
	rows, err := d.Store.MacroHighEventsIn(ctx, now.Add(-24*time.Hour), now.Add(48*time.Hour), nil)
	if err != nil {
		log.Printf("[macro:actuals] seed scan: %v", err)
		return
	}
	for _, e := range rows {
		seen[e.ID] = e.Actual
		if e.Actual != "" && now.Sub(e.EventTime) <= actualsSeenInitialFresh {
			pushActual(ctx, d, e, now)
		}
	}
}

// sweepActuals 空→非空判定推送：seen 记 ""（或未见过）∧ 现值非空 → 推 + 更新。
// 启动后未 seed 的行（rearm 早期信号）视同 ""（actual 非空首见即推——事件刚落库场景）。
func sweepActuals(ctx context.Context, d Deps, seen map[int64]string, now time.Time) {
	rows, err := d.Store.MacroHighEventsIn(ctx, now.Add(-24*time.Hour), now.Add(48*time.Hour), nil)
	if err != nil {
		log.Printf("[macro:actuals] sweep scan: %v", err)
		return
	}
	for _, e := range rows {
		if _, ok := seen[e.ID]; !ok && e.Actual == "" {
			seen[e.ID] = "" // 新空行记位（出值后下轮推）
			continue
		}
		if e.Actual != "" && seen[e.ID] == "" {
			pushActual(ctx, d, e, now)
			seen[e.ID] = e.Actual
		}
	}
}

// pushActual 推送一行：飞书 announce + alerts dedup（同文案 4h 防重；写失败只 log）。
func pushActual(ctx context.Context, d Deps, e pgstore.MacroEvent, now time.Time) {
	bj := e.EventTime.In(bjLoc).Format("01-02 15:04")
	msg := fmt.Sprintf("[macro] %s %s %s：实际 %s（预期 %s / 前值 %s）",
		bj, e.Currency, e.Name, e.Actual, dashIfEmpty(e.Forecast), dashIfEmpty(e.Previous))
	announce(ctx, d, msg)
	if err := d.Store.InsertAlertDedup(ctx, "info", msg, actualPushDedup); err != nil {
		log.Printf("[macro:actuals] dedup alert: %v", err)
	}
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// fetchCalendarDay 拉当日（BJ 00:00 起 24h）窗口 upsert（actualsLoop 到点路径）。
// 端点窗语义见 fetch_mql5.go 头注释：from/to 为日粒度选择，多回 ±1~2 天幂等无害。
func fetchCalendarDay(ctx context.Context, d Deps, client *http.Client, now time.Time) error {
	bj := now.In(bjLoc)
	dayStart := time.Date(bj.Year(), bj.Month(), bj.Day(), 0, 0, 0, 0, bjLoc)
	to := dayStart.Add(24 * time.Hour).Add(-time.Second)
	events, err := FetchMQL5Calendar(ctx, client, mql5URL, dayStart, to)
	if err != nil {
		return err
	}
	n, err := d.Store.UpsertMacroEvents(ctx, events)
	if err != nil {
		return err
	}
	log.Printf("[macro:actuals] day upsert %d events (%s)", n, dayStart.Format("01-02"))
	return nil
}
