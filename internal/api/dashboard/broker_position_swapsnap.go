package dashboard

import (
	"context"
	"log"
	"time"

	"maps"

	"nox/internal/data/venue"
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

// swapFrameItem 票⑤′：帧锚定 swap 落库条目——order=帧携载荷，at=帧到达时刻（行 ts）。
type swapFrameItem struct {
	order venue.Order
	at    time.Time
}

// collectSwapFrames 票⑤′：drain OnMT5Frame 排队的帧携 swap 变更并落库。
// DB 写走 Run 循环（流 goroutine 零阻塞）；帧到达时刻 at 透传为行 ts=帧锚定。
func (m *BrokerPositionMonitor) collectSwapFrames(ctx context.Context) {
	m.mu.Lock()
	items := make([]swapFrameItem, 0, len(m.pendingSwap))
	for _, it := range m.pendingSwap {
		items = append(items, it)
	}
	m.pendingSwap = make(map[string]swapFrameItem)
	m.mu.Unlock()
	if len(items) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, bpFlushBound) // 同 collectSwapSnapshots 批量界
	defer cancel()
	m.recordSwapItems(ctx, items)
}

// swap=0 也写（B2 修订）：Exness swap 恒=0，首次写0后不再变化=去重不写新行，但至少有一条基准行。
// D-180 S1：swap 值变化时检测跳变 → 收集 swapJump → poll 末尾聚合写 swap_settled facts。
func (m *BrokerPositionMonitor) collectSwapSnapshots(ctx context.Context) {
	if m.swapSnapStore == nil {
		return
	}
	// D-575：DB 批量写整体界——下游 InsertSwapSnapshot/buildPositionIDIndex/RecordJumps 自动继承。
	ctx, cancel := context.WithTimeout(ctx, bpFlushBound)
	defer cancel()
	// 复制 venues 快照（锁内复制，锁外处理）
	m.mu.RLock()
	venues := maps.Clone(m.venues)
	m.mu.RUnlock()

	now := time.Now().UTC()
	var items []swapFrameItem
	for _, vs := range venues {
		if !vs.online {
			continue
		}
		for _, o := range vs.orders {
			items = append(items, swapFrameItem{order: o, at: now})
		}
	}
	m.recordSwapItems(ctx, items)
}

// recordSwapItems 票⑤′：REST 回读批（at=回读时刻）与 MT 帧批（at=帧到达时刻）共用的
// 落库+跳变检测径——posID 索引/去重/insert/RecordJumps 单点，两源差只在 ts 锚。
func (m *BrokerPositionMonitor) recordSwapItems(ctx context.Context, items []swapFrameItem) {
	idx, _ := buildPositionIDIndex(ctx, m.swapSnapStore)
	var jumps []swapJump // D-180 S1：本轮检测到的跳变
	for _, it := range items {
		o := it.order
		posID := idx[o.VenueKey+"\x00"+o.Ticket]
		if posID == 0 {
			continue // 孤儿单/未关联
		}
		key := o.VenueKey + "\x00" + o.Ticket
		prev, hasPrev := m.lastSwapByTicket[key]
		if hasPrev && prev == o.Swap {
			continue // 去重：swap 值未变化
		}
		if err := m.swapSnapStore.InsertSwapSnapshot(ctx, posID, o.VenueKey, o.Swap, it.at); err != nil {
			log.Printf("[broker-mon] swap snapshot insert posID=%d venue=%s: %v", posID, o.VenueKey, err)
			continue
		}
		// D-180 S1：跳变检测——有前值且值变化 → 记录跳变（首次写入无前值不算跳变）。
		if hasPrev && prev != o.Swap && m.settlementCollector != nil {
			if j, ok := detectRealtimeJump(m.swapSnapStore, posID, o, prev, it.at); ok {
				jumps = append(jumps, j)
			}
		}
		m.lastSwapByTicket[key] = o.Swap
	}
	// D-180 S1：聚合写 swap_settled facts（三倍日剔除+中位数+幂等）。
	if m.settlementCollector != nil && len(jumps) > 0 {
		m.settlementCollector.RecordJumps(ctx, jumps)
	}
}
