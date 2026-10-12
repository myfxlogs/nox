package dashboard

import (
	"time"

	"nox/internal/data/bus"
)

// D-493 S1：账户卡片报价健康透出——复用 MT 健康监控同源数据（qbus.AllLatest）与阈值，
// 不新增轮询。阈值与 config.MTData HealthQuoteWarn/Crit 缺省一致（5m/30m）。
const (
	feedQuoteWarn = 5 * time.Minute
	feedQuoteCrit = 30 * time.Minute
)

// quoteAgeByVenue 从 qbus 按 Venue 分组取 max(Time)，返回 venue→age（与 mt-health 同推导）。
func quoteAgeByVenue(qbus *bus.QuoteBus, now time.Time) map[string]time.Duration {
	out := make(map[string]time.Duration)
	if qbus == nil {
		return out
	}
	latest := make(map[string]time.Time)
	for _, q := range qbus.AllLatest() {
		if q.Time.IsZero() {
			continue
		}
		if t, ok := latest[q.Venue]; !ok || q.Time.After(t) {
			latest[q.Venue] = q.Time
		}
	}
	for v, t := range latest {
		out[v] = now.Sub(t)
	}
	return out
}

// feedStateOf 报价龄→三态：live / stale / dark。age<=0 = 无样本 → dark + ageSec -1。
func feedStateOf(age time.Duration) (state string, ageSec float64) {
	switch classifyAge(age, feedQuoteWarn, feedQuoteCrit) {
	case metricHealthy:
		state = "live"
	case metricWarn:
		state = "stale"
	default:
		state = "dark"
	}
	if age <= 0 {
		return state, -1
	}
	return state, age.Seconds()
}
