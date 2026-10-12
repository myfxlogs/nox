// position_monitor_replay.go —— posMon 回放源+流订阅口
// （自 position_monitor.go 拆出保行数；锁纪律：锁内读 latest 快照，锁外交付 caller）。
package dashboard

import (
	"context"

	v1 "nox/internal/gen/dashboardv1"
)

// healthKey SourceHealth 行键（WS 源=source url 段；空回退 venue）。
func healthKey(h *v1.SourceHealth) string {
	if h.Source != "" {
		return h.Source
	}
	return h.Venue
}

// PositionEvents case4 回放源（latest 全量逐行 upsert；首次访问触发 boot）。
func (m *PositionMonitor) PositionEvents(ctx context.Context) []*v1.Position {
	m.ensureBoot(ctx)
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*v1.Position, 0, len(m.latestPos))
	for _, k := range sortedKeys(m.latestPos) {
		out = append(out, m.latestPos[k])
	}
	return out
}

// HealthEvents case5 回放源。
func (m *PositionMonitor) HealthEvents(ctx context.Context) []*v1.SourceHealth {
	m.ensureBoot(ctx)
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*v1.SourceHealth, 0, len(m.latestHealth))
	for _, h := range m.latestHealth {
		out = append(out, h)
	}
	return out
}

// ensureBoot 回放口惰性 boot（首个流连接到达时组装 latest——boot 段不广播）。
func (m *PositionMonitor) ensureBoot(ctx context.Context) {
	m.mu.RLock()
	booted := m.booted
	m.mu.RUnlock()
	if !booted {
		m.boot(ctx)
	}
}

// SubscribePositions case4 订阅口（service_stream 用；nil-safe 返回 nil channel）。
func (m *PositionMonitor) SubscribePositions() <-chan *v1.Position {
	if m == nil || m.evPos == nil {
		return nil
	}
	ch, _ := m.evPos.Subscribe()
	return ch
}

// SubscribeHealth case5 订阅口。
func (m *PositionMonitor) SubscribeHealth() <-chan *v1.SourceHealth {
	if m == nil || m.evHealth == nil {
		return nil
	}
	ch, _ := m.evHealth.Subscribe()
	return ch
}

// SetPositionMonitor 注入四域发射器（main 装配；回放/流订阅经此访问）。
func (s *Service) SetPositionMonitor(m *PositionMonitor) { s.posMon = m }
