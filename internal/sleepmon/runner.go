// sleepmon —— D-534 B4：睡钱触发层（议题集裁决 7d 窗钉常量）。
// BJ 历日每 30min tick 检查「今日未跑」→ 拉 ExplorerIdleFunds 逐池计
// consecutive_below_days；连破地板 >=7 天且本轮未告警 → warn 级 InsertAlert +
// 收件箱提案行（source=sleep_money，动钱永不自动，再配置走业主通道）。
// 非 below → streak 归零+alerted 复位（本轮 resolved 下轮重计）。
// 只写 idle_floor_streak 自表+alert+inbox，无外部副作用。
package sleepmon

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/inboxfmt"
	"arb/internal/store/pgstore"
)

// Store 依赖面（pgstore.Store 实现；测试 fake）。
type Store interface {
	ExplorerIdleFunds(ctx context.Context, now time.Time) ([]pgstore.ExplorerIdlePool, error)
	GetStreaks(ctx context.Context) (map[string]pgstore.IdleStreak, error)
	UpsertStreak(ctx context.Context, venue, venueType string, days int, checkDay time.Time, alerted bool) error
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// Deps 装配依赖。
type Deps struct {
	Store     Store
	InboxPath string         // 收件箱路径（同 sysretro DefaultInboxPath）
	Tick      time.Duration  // 缺省 30min
	Loc       *time.Location // BJ 时区；nil=Asia/Shanghai 固定 +8
}

// belowFloorAlertDays 议题集裁决钉值（D-534 设计稿；7d 连续才告警，业务假设钉码出处=KB
// business-assumptions-in-code-audit 口径——常量+注释）。
const belowFloorAlertDays = 7

var bjLoc = time.FixedZone("BJ", 8*3600)

// Run 阻塞循环：BJ 历日切日即跑一次 checkDay（tick 缺省 30min，错过窗口下 tick 补）。
func Run(ctx context.Context, deps Deps) {
	tick := deps.Tick
	if tick <= 0 {
		tick = 30 * time.Minute
	}
	loc := deps.Loc
	if loc == nil {
		loc = bjLoc
	}
	var lastRunDay string
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		day := time.Now().In(loc).Format("2006-01-02")
		if day != lastRunDay {
			if err := checkDay(ctx, deps, day, loc); err != nil {
				log.Printf("[sleepmon] check %s: %v", day, err)
			} else {
				lastRunDay = day
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// checkDay 当日一次全池检查（幂等：同日重跑 last_check_day 相同不重复计天）。
func checkDay(ctx context.Context, deps Deps, day string, loc *time.Location) error {
	checkDay, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil {
		return err
	}
	pools, err := deps.Store.ExplorerIdleFunds(ctx, time.Now())
	if err != nil {
		return fmt.Errorf("idle funds: %w", err)
	}
	streaks, err := deps.Store.GetStreaks(ctx)
	if err != nil {
		return err
	}
	for _, p := range pools {
		key := p.Venue + "\x00" + p.VenueType
		st := streaks[key]
		sameDay := st.CheckDay.Format("2006-01-02") == day
		days, alerted := st.Days, st.Alerted
		if p.BelowFloor {
			if !sameDay {
				days++
			}
			if days >= belowFloorAlertDays && !alerted {
				alerted = true
				msg := fmt.Sprintf("[sleepmon] 睡钱告警 %s/%s：余额 %.2f %s 连续 %d 天低于地板线（缓冲 %.2f），近 30 日收益 %.2f",
					p.Venue, p.VenueType, p.Balance, p.Currency, days, p.BufferMultiple, p.Income30dUSD)
				if err := deps.Store.InsertAlert(ctx, nil, "warn", msg); err != nil {
					log.Printf("[sleepmon] alert %s/%s: %v", p.Venue, p.VenueType, err)
				}
				reason := fmt.Sprintf("余额 %.2f 连续 %d 天低于地板线，近 30 日收益 %.2f", p.Balance, days, p.Income30dUSD)
				if err := inboxfmt.Append(deps.InboxPath, day, []inboxfmt.Row{{
					Source: inboxfmt.SrcSleepMoney, Title: p.Venue + "/" + p.VenueType,
					Reason: reason, Action: "再配置决策走业主通道",
				}}); err != nil {
					log.Printf("[sleepmon] inbox %s/%s: %v", p.Venue, p.VenueType, err)
				}
			}
		} else {
			days, alerted = 0, false // 本轮 resolved：归零重开才复位
		}
		if days != st.Days || alerted != st.Alerted || !sameDay {
			if err := deps.Store.UpsertStreak(ctx, p.Venue, p.VenueType, days, checkDay, alerted); err != nil {
				return fmt.Errorf("streak upsert %s/%s: %w", p.Venue, p.VenueType, err)
			}
		}
	}
	return nil
}
