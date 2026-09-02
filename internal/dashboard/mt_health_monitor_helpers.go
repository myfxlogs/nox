package dashboard

import (
	"context"
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

// isMTWeekendQuietWindow MT 周末休市静默窗（Fri 20:55 UTC → Sun 22:10 UTC，D-136 修复②）。
// 休市窗内 quote/margin 陈旧=预期（收盘无报价），告警静默；窗两端 buffer：
// 收盘前 5min（收尾报价稀疏）/开盘后 10min（恢复迟滞）。
// 仅静默告警插入——freshness fail-closed 门禁不受影响（交易安全不降级）。
// 已知取舍：法定假日（圣诞等）不在窗口内，罕见，人工按需 ack。
func isMTWeekendQuietWindow(t time.Time) bool {
	t = t.UTC()
	minutes := t.Hour()*60 + t.Minute()
	switch t.Weekday() {
	case time.Friday:
		return minutes >= 20*60+55
	case time.Saturday:
		return true
	case time.Sunday:
		return minutes < 22*60+10
	default:
		return false
	}
}

// nowTime 返回当前时钟（可注入，nil→time.Now，D-136 修复②）。
func (m *MTHealthMonitor) nowTime() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// collectQuoteAges 从 qbus 按 Venue 分组取 max(Time)，返回 venue→age。
func (m *MTHealthMonitor) collectQuoteAges(now time.Time) map[string]time.Duration {
	out := make(map[string]time.Duration)
	if m.qbus == nil {
		return out
	}
	latest := make(map[string]time.Time)
	for _, q := range m.qbus.AllLatest() {
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

// collectAliases 从 store 查 venue→alias 映射（D-203：告警/日志用别名）。
// 查询失败 → 返回空 map（回退裸账号，不阻断）。
func (m *MTHealthMonitor) collectAliases(ctx context.Context) map[string]string {
	out := make(map[string]string)
	if m.store == nil {
		return out
	}
	aliases, err := m.store.ListAccountAliases(ctx)
	if err != nil {
		return out // 回退裸账号
	}
	return aliases
}

// aliasOrVenue 有别名用别名，无别名回退裸 venue（D-203）。
func aliasOrVenue(venue string, aliases map[string]string) string {
	if a, ok := aliases[venue]; ok && a != "" {
		return a
	}
	return venue
}
