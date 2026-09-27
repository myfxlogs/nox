// schedule.go 触发时刻表（D-513 设计稿 §1，sysretro NextDelay 同构）：
// daily=每日 06:47 北京时间（digest 06:10 之后取新鲜收件箱产物；避开整点/半点）；
// weekly=每周日 07:13 北京时间（CFD 周末静默窗，研究无交易噪声）。
// 时区=BJTime FixedZone（禁 LoadLocation）；scan_date 由 Go 侧按 BJ 历日算（F1：
// DB timezone=Etc/UTC，proposed_at::date 日界=UTC 午夜=BJ 08:00——钉代码不赌 DB）。
package explorer

import "time"

// NextDelay 距下一触发时刻的时长（now 恰过触发点则取下一周期）。
func NextDelay(now time.Time, kind string) time.Duration {
	bjNow := now.In(BJTime)
	if kind == "weekly" {
		next := time.Date(bjNow.Year(), bjNow.Month(), bjNow.Day(), 7, 13, 0, 0, BJTime)
		for next.Weekday() != time.Sunday || !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return next.Sub(now)
	}
	next := time.Date(bjNow.Year(), bjNow.Month(), bjNow.Day(), 6, 47, 0, 0, BJTime)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(now)
}

// ScanDate now 的 BJ 历日（explorer_proposals.scan_date 写入值——F1 钉死）。
func ScanDate(now time.Time) time.Time {
	bj := now.In(BJTime)
	return time.Date(bj.Year(), bj.Month(), bj.Day(), 0, 0, 0, 0, time.UTC)
}
