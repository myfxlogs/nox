// run.go —— D-033：自盯调度（看门狗形态——timer 定点唤醒，数据全在触发时刻
// 单查 DB；任何周期 ticker 不携带/刷新业务状态）。
// 节奏：日报 08:00 BJ（周一兼发周报=原 D-032 ④ 并入本面）；异常体检每小时正点唤醒。
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
	log.Printf("[selfwatch] scheduler started (daily 08:00BJ paper+stage · mon 08:00BJ weekly · hourly anomaly)")
	<-ctx.Done()
}

// dailyLoop 每日 08:00 BJ：paper 窗日报+Stage 表 → 落档/告警/飞书；
// 全项 ✅ 时附「待决策：Stage 1 可启」独立 info 告警；周一同点加发周报（D-032 ④ 移入）。
func dailyLoop(ctx context.Context, d Deps) {
	for {
		next := NextDailyBJ(d.now(), 8, 0)
		if !sleepUntil(ctx, next) {
			return
		}
		rep, err := BuildDaily(ctx, d)
		if err != nil {
			log.Printf("[selfwatch] daily build: %v", err)
		} else {
			log.Printf("[selfwatch] daily report:\n%s", rep.Body)
			d.deliver(ctx, "daily", "info", rep.Body)
			if rep.Stage1OK {
				const readyMsg = "[自盯] 待决策：Stage 1 可启（paper 窗≥7d 净额非负·零事故·子系统在线·cap/白名单已定）"
				if err := d.Store.InsertAlert(ctx, nil, "info", readyMsg); err != nil {
					log.Printf("[selfwatch] stage1 ready alert: %v", err)
				}
				// info 级不进告警 hook——决策推送走显式 SendText（同周报通道）。
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

// anomalyLoop 数据面体检：每小时正点唤醒一次（去重窗 1h 与之对齐——
// 同键在窗内只落一行，窗过复报=持续异常仍有节奏可见）。
func anomalyLoop(ctx context.Context, d Deps) {
	for {
		next := d.now().Truncate(time.Hour).Add(time.Hour)
		if !sleepUntil(ctx, next) {
			return
		}
		CheckAnomalies(ctx, d)
	}
}
