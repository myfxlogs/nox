package dashboard

// MTHealthMonitor 告警构造/格式化（D-261 S1 从 mt_health_monitor.go 拆出，纯搬家）。

import (
	"context"
	"log"
	"time"
)

// critRealertInterval crit 持续同态重告警去重窗口（D-104 P0 返修③ R-c3）：持续 crit 每 30min 重发一条（有界，不洪水）；boot 首 tick 静默记 crit，tick2 转换式告警触发。
const critRealertInterval = 30 * time.Minute

// maybeAlert 状态转换时插告警（healthy→warn/crit；stale→healthy 插 info recovered）；持续同态不重复插（防洪水），crit >30min 重发一条（R-c3）；D-136：周末休市静默窗内告警静默，状态机照常运转。
func (m *MTHealthMonitor) maybeAlert(ctx context.Context, venue, metric string, prev, cur mtHealthMetric, age time.Duration) {
	if isMTWeekendQuietWindow(m.nowTime()) {
		return // 周末休市静默窗
	}
	if prev == cur {
		// R-c3：持续 crit 超过去重窗口 → 重发一条（warn/healthy 维持纯转换语义不变）。
		if cur != metricCrit {
			return
		}
		key := venue + ":" + metric
		m.mu.Lock()
		last, ok := m.lastCritAlert[key]
		m.mu.Unlock()
		if ok && m.nowTime().Sub(last) < critRealertInterval {
			return // 窗口内不重发
		}
	}
	var level, msg string
	subject := "MT5 数据监控：" + venue + " " + mtMetricCN(metric)
	switch cur {
	case metricHealthy:
		level = "info"
		msg = subject + "已恢复（此前停更 " + durStr(age) + "）"
	case metricWarn:
		level = "warn"
		msg = subject + "数据已停更（" + durStr(age) + " 未更新）→ 检查该端连接/行情源"
	case metricCrit:
		level = "critical"
		msg = subject + "数据严重停更（" + durStr(age) + " 未更新）→ 需立即人工介入"
	}
	if cur == metricCrit {
		key := venue + ":" + metric
		m.mu.Lock()
		m.lastCritAlert[key] = m.nowTime()
		m.mu.Unlock()
	}
	if err := m.store.InsertAlert(ctx, nil, level, msg); err != nil {
		log.Printf("[mt-health] alert insert: %v", err)
	}
	log.Printf("[mt-health] %s: %s", level, msg)
}

// mtMetricCN 监控项中文化（D-573 词表：quote/swap/margin；未知值原样透出）。
func mtMetricCN(metric string) string {
	switch metric {
	case "quote":
		return "行情报价"
	case "swap":
		return "隔夜息"
	case "margin":
		return "保证金"
	}
	return metric
}
