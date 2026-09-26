// schedule.go 触发时刻表（D-457 S3；D-510 整体切北京历日）：
// daily=每日 00:30 北京时间（保留 30min 数据到齐缓冲）；weekly=每周一 00:30 北京时间。
// 时区=time.FixedZone("Asia/Shanghai", 8*3600)——禁 time.LoadLocation（alpine 容器无
// tzdata 会 panic）；中国 1991 起无 DST，固定偏移正确。
// 语义红利：北京 00:00=UTC 16:00=binance 8h funding 结算边界——北京历日窗装 3 个完整结算窗。
package sysretro

import "time"

// BJTime 北京时区（FixedZone，D-510 S1）。
var BJTime = time.FixedZone("Asia/Shanghai", 8*3600)

// NextDelay 距下一触发时刻的时长（now 恰过触发点则取下一周期）。
// now 可为任意时区时刻；目标时刻按北京历日计算。
func NextDelay(now time.Time, kind string) time.Duration {
	bjNow := now.In(BJTime)
	if kind == "weekly" {
		next := time.Date(bjNow.Year(), bjNow.Month(), bjNow.Day(), 0, 30, 0, 0, BJTime)
		for next.Weekday() != time.Monday || !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return next.Sub(now)
	}
	next := time.Date(bjNow.Year(), bjNow.Month(), bjNow.Day(), 0, 30, 0, 0, BJTime)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(now)
}
