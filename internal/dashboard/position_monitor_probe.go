// position_monitor_probe.go —— D-047③：探针仓到期平仓的唤醒面。
// 事件驱动形态：posChange/flush 时重排「最早 auto_close_at」定时器——定时器
// 只叫醒不携数据；醒来由 ExpireDueProbes 查 DB 实时态决策（到期/撤单/已平
// 皆由查询裁定，timer 持值过期不影响正确性）。仓变更（开/平/新探针）→
// positions 域脏 → flush → refreshProbeWake 重排=自然收敛。
package dashboard

import (
	"context"
	"log"
	"time"
)

// refreshProbeWake 重排探针到期定时器（flush/boot 后调用）。
// 查询失败不臂表（下轮 flush 补）；最早到期刻挪前/挪后都重臂。
func (m *PositionMonitor) refreshProbeWake(ctx context.Context) {
	if m.s == nil {
		return
	}
	next, err := m.s.NextProbeExpiryAt(ctx)
	if err != nil {
		log.Printf("[posMon] probe expiry query: %v", err)
		return
	}
	m.mu.Lock()
	if m.probeT != nil {
		m.probeT.Stop()
		m.probeT = nil
	}
	if next == nil {
		m.mu.Unlock()
		return
	}
	d := time.Until(*next)
	if d < 0 {
		d = 0
	}
	m.probeT = time.AfterFunc(d, func() {
		// 仅唤醒：决策=ExpireDueProbes 查 DB（D-004 定时器不携业务数据）。
		pctx, cancel := context.WithTimeout(context.Background(), bpFlushBound)
		defer cancel()
		m.s.ExpireDueProbes(pctx)
		m.refreshProbeWake(pctx) // 平后重排下一枚（含幂等补臂）
		m.KickPositions()        // 仓态变更扇出 positions 域
	})
	m.mu.Unlock()
}
