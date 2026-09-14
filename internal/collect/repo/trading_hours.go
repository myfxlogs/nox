// trading_hours.go A 股交易时段判断（北京时间 9:30-15:00 周一至五 + cnfi_calendar 非节假日）。
// 从 collector.go 拆出（D-275 纯重构）。
package repo

import (
	"context"
	"log"
	"time"
)

// isTradingTime 判断是否在 A 股交易时段（北京时间 9:30-15:00 周一至五 + cnfi_calendar 非节假日）。
// 日历缺失日=保守跳过+log（宁可漏采不可写脏值，设计 §2.1）。
func (c *RepoCollector) isTradingTime(ctx context.Context, now time.Time) bool {
	// 周末跳过
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	// 交易时段 9:30-15:00 北京时间
	h, m := now.Hour(), now.Minute()
	clockMin := h*60 + m
	if clockMin < 9*60+30 || clockMin >= 15*60 {
		return false
	}
	// cnfi_calendar 节假日过滤（日历缺失日=保守跳过）
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, c.location)
	has, err := c.store.CNFICalendarHasDate(ctx, today)
	if err != nil {
		log.Printf("repo: calendar query: %v（日历查询失败，保守跳过）", err)
		return false
	}
	if !has {
		log.Printf("repo: calendar_unknown for %s（保守跳过，F4）", today.Format("2006-01-02"))
		return false
	}
	var business bool
	if err := c.store.Pool().QueryRow(ctx,
		`SELECT is_business_day FROM cnfi_calendar WHERE cal_date=$1`,
		today.Format("2006-01-02")).Scan(&business); err != nil {
		log.Printf("repo: calendar is_business_day query: %v（保守跳过）", err)
		return false
	}
	if !business {
		return false
	}
	return true
}
