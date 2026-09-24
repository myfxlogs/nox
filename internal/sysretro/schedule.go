// schedule.go 触发时刻表（D-457 S3）：daily=每日 00:30 UTC；weekly=每周一 01:00 UTC。
package sysretro

import "time"

// NextDelay 距下一触发时刻的时长（now 恰过触发点则取下一周期）。
func NextDelay(now time.Time, kind string) time.Duration {
	if kind == "weekly" {
		next := time.Date(now.Year(), now.Month(), now.Day(), 1, 0, 0, 0, time.UTC)
		for next.Weekday() != time.Monday || !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return next.Sub(now)
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 30, 0, 0, time.UTC)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(now)
}
