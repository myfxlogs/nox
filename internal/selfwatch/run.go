// run.go —— D-033：自盯调度（看门狗形态——timer 定点唤醒，数据全在触发时刻
// 单查 DB；任何周期 ticker 不携带/刷新业务状态）。
// 节奏：日报 08:30 BJ（D-056③——晚于 00:05Z 纸面轮，含当日 00:00Z 结算后计息；
// 周一兼发周报=原 D-032 ④ 并入本面）；异常体检每小时正点唤醒。
package selfwatch

import (
	"context"
	"log"
	"time"

	"arb/internal/safego"
)

// Run 阻塞至 ctx 取消（两路子 goroutine 经 safego 托管）。
func Run(ctx context.Context, d Deps) {
	safego.Go("selfwatch:daily", func() { dailyLoop(ctx, d) })
	safego.Go("selfwatch:anomaly", func() { anomalyLoop(ctx, d) })
	log.Printf("[selfwatch] scheduler started (daily 08:30BJ paper+stage · mon 08:30BJ weekly · hourly anomaly)")
	<-ctx.Done()
}

// dailyLoop 每日 08:30 BJ：paper 窗日报+Stage 表 → 落档/告警/飞书；
// 全项 ✅ 时附「待决策：Stage 1 可启」独立 info 告警；周一同点加发周报（D-032 ④ 移入）。
func dailyLoop(ctx context.Context, d Deps) {
	for {
		next := NextDailyBJ(d.now(), 8, 30)
		if !sleepUntil(ctx, next) {
			return
		}
		rep, err := BuildDaily(ctx, d)
		if err != nil {
			log.Printf("[selfwatch] daily build: %v", err)
		} else {
			log.Printf("[selfwatch] daily report:\n%s", rep.Body)
			d.deliver(ctx, "daily", "info", rep.Body)
			emitStage1Ready(ctx, d, rep.Stage1OK)
		}
		if next.In(bj).Weekday() == time.Monday && d.Weekly != nil {
			text, err := d.Weekly(ctx, d.now())
			if err != nil {
				log.Printf("[selfwatch] weekly build: %v", err)
			} else {
				log.Printf("[selfwatch] weekly report:\n%s", text)
				d.deliver(ctx, "weekly", "info", text)
			}
		}
	}
}

// emitStage1Ready R1②：就绪推送仅 ❌→✅ 转换时一次——cfgStage1Ready 存上轮就绪态
// （app_config 跨重启存活）；❌→✅ 时 InsertAlertDedup（消息键含 stage1_ready·窗 7d
// 兜底抑频）+ 显式飞书直推（info 级不进告警 hook）。上轮态读败→视同未就绪按转换发
// （7d 去重防重复轰炸）；态更新每轮都写（含 ✅→❌ 回落，下次回绿再发一次）。
func emitStage1Ready(ctx context.Context, d Deps, ready bool) {
	cur := "0"
	if ready {
		cur = "1"
	}
	prev, pok, perr := d.Store.AppConfigString(ctx, cfgStage1Ready)
	if perr != nil {
		log.Printf("[selfwatch] stage1 ready state read: %v", perr)
	}
	if ready && !(perr == nil && pok && prev == "1") {
		// D-061 ③：就绪推送条件=stage1Checks 全 ✅（条件制判据），文案同步条件制口径。
		const readyMsg = "[自盯][stage1_ready] 待决策：Stage 1 可启（目标组合纸面行存活且计息·开仓时点合格·组合净额非负·零事故·子系统在线·仓位上限/白名单已定）"
		if err := d.Store.InsertAlertDedup(ctx, "info", readyMsg, 7*24*time.Hour); err != nil {
			log.Printf("[selfwatch] stage1 ready alert: %v", err)
		}
		if d.Notify != nil && d.Notify.Enabled() && d.ChatID != nil {
			if chatID, cerr := d.ChatID(ctx); cerr != nil {
				log.Printf("[selfwatch] stage1 ready chat id: %v", cerr)
			} else if chatID != "" {
				if serr := d.Notify.SendText(ctx, chatID, readyMsg); serr != nil {
					log.Printf("[selfwatch] stage1 ready push: %v", serr)
				}
			}
		}
	}
	if err := d.Store.UpsertAppConfig(ctx, cfgStage1Ready, cur, "selfwatch stage1 ready state"); err != nil {
		log.Printf("[selfwatch] stage1 ready state write: %v", err)
	}
}

// anomalyLoop 数据面体检：每小时正点唤醒一次（去重窗 1h 与之对齐——
// 同键在窗内只落一行，窗过复报=持续异常仍有节奏可见）。
func anomalyLoop(ctx context.Context, d Deps) {
	for {
		next := d.now().Truncate(time.Hour).Add(time.Hour)
		if !sleepUntil(ctx, next) {
			return
		}
		CheckAnomalies(ctx, d)
		// D-071 C：Stage 1 就绪改小时级评估——条件制判据（D-061）小时级变化，
		// 日报 08:30 BJ 一天一次会错过整窗。evalStage1 纯读面零副作用（不落档/
		// 不写 app_config/不发日报），emitStage1Ready 自带 ❌→✅ 单次+7d dedup。
		if rows, _, _, _, err := evalStage1(ctx, d, d.now()); err != nil {
			log.Printf("[selfwatch] hourly stage1 eval: %v", err)
		} else {
			emitStage1Ready(ctx, d, allOK(rows))
		}
	}
}
