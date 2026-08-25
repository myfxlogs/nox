package dashboard

import (
	"strings"
	"time"
)

// classifyAge 按阈值分类三态。age<=0（无数据）→ crit（从未采集=异常）。
func classifyAge(age time.Duration, warn, crit time.Duration) mtHealthMetric {
	if age <= 0 {
		return metricCrit // 无数据=异常
	}
	if age >= crit {
		return metricCrit
	}
	if age >= warn {
		return metricWarn
	}
	return metricHealthy
}

// venueFromKey 从 "{venue}:{venueType}" 提取 venue 部分。
func venueFromKey(vk string) string {
	if i := strings.IndexByte(vk, ':'); i >= 0 {
		return vk[:i]
	}
	return vk
}

// durStr 时间格式化（<1s 显示秒精度，否则分钟级）。
func durStr(d time.Duration) string {
	if d <= 0 {
		return "n/a"
	}
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}
