package dashboard

import (
	"context"
	"log"
	"time"

	"maps"
)

// SwapSnapshotStore D-110 P2 swap 快照采集依赖（setter 注入，避免 NewBrokerPositionMonitor 签名变更）。
type SwapSnapshotStore interface {
	InsertSwapSnapshot(ctx context.Context, posID int64, venue string, swapUSD float64, ts time.Time) error
	PositionLegLister
}

// SetSwapSnapshotStore D-110 P2 注入 swap 快照采集依赖（boot 时调用）。
func (m *BrokerPositionMonitor) SetSwapSnapshotStore(s SwapSnapshotStore) {
	m.swapSnapStore = s
	m.lastSwapByTicket = make(map[string]float64)
}

// swap=0 也写（B2 修订）：Exness swap 恒=0，首次写0后不再变化=去重不写新行，但至少有一条基准行。
// D-180 S1：swap 值变化时检测跳变 → 收集 swapJump → poll 末尾聚合写 swap_settled facts。
func (m *BrokerPositionMonitor) collectSwapSnapshots(ctx context.Context) {
	if m.swapSnapStore == nil {
		return
	}
	// 复制 venues 快照（锁内复制，锁外处理）
	m.mu.RLock()
	venues := maps.Clone(m.venues)
	m.mu.RUnlock()

	idx, _ := buildPositionIDIndex(ctx, m.swapSnapStore)
	now := time.Now().UTC()
	var jumps []swapJump // D-180 S1：本轮检测到的跳变
	for _, vs := range venues {
		if !vs.online {
			continue
		}
		for _, o := range vs.orders {
			posID := idx[o.VenueKey+"\x00"+o.Ticket]
			if posID == 0 {
				continue // 孤儿单/未关联
			}
			key := o.VenueKey + "\x00" + o.Ticket
			prev, hasPrev := m.lastSwapByTicket[key]
			if hasPrev && prev == o.Swap {
				continue // 去重：swap 值未变化
			}
			if err := m.swapSnapStore.InsertSwapSnapshot(ctx, posID, o.VenueKey, o.Swap, now); err != nil {
				log.Printf("[broker-mon] swap snapshot insert posID=%d venue=%s: %v", posID, o.VenueKey, err)
				continue
			}
			// D-180 S1：跳变检测——有前值且值变化 → 记录跳变（首次写入无前值不算跳变）。
			if hasPrev && prev != o.Swap && m.settlementCollector != nil {
				if j, ok := detectRealtimeJump(m.swapSnapStore, posID, o, prev, now); ok {
					jumps = append(jumps, j)
				}
			}
			m.lastSwapByTicket[key] = o.Swap
		}
	}
	// D-180 S1：聚合写 swap_settled facts（三倍日剔除+中位数+幂等）。
	if m.settlementCollector != nil && len(jumps) > 0 {
		m.settlementCollector.RecordJumps(ctx, jumps)
	}
}
