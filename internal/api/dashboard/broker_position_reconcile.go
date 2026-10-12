// broker_position_reconcile.go D-167 折分：kick drain+REST 权威回读段自
// broker_position_events.go 提取保行数（零行为变更，纯搬函数）。
package dashboard

import (
	"context"
	"log"
	"time"

	"arb/internal/data/keydomain"
)

// drainKicks 取走并清空待回读 venue 集（合并语义：集合去重）。
func (m *BrokerPositionMonitor) drainKicks() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.pendingKick))
	for vk := range m.pendingKick {
		out = append(out, vk)
	}
	m.pendingKick = make(map[string]struct{})
	return out
}

// reconcileAll 全 venue 权威回读（boot/Refresh/泛 kick 用）。
func (m *BrokerPositionMonitor) reconcileAll(ctx context.Context) {
	for _, r := range m.bm.venueOrderResults(ctx) {
		m.applyVenueResult(ctx, r)
	}
	m.collectSwapSnapshots(ctx)
}

// reconcileVenueKeys 按 kick 键回读：完整 venueKey 直查；裸 venue 名回读其全部类型键。
func (m *BrokerPositionMonitor) reconcileVenueKeys(ctx context.Context, kick string) {
	hit := false
	for _, q := range m.bm.queriers() {
		if q == nil {
			continue
		}
		vk := q.VenueKey()
		if vk == kick || keydomain.BareOf(vk) == kick {
			m.applyVenueResult(ctx, m.bm.venueOrders(ctx, vk))
			hit = true
		}
	}
	if !hit {
		m.requeueKickIfAttaching(kick)
		return
	}
	m.collectSwapSnapshots(ctx)
}

// requeueKickIfAttaching D-164 R5：attach-pending 感知重排——miss 且该 venue attach
// 进行中（Connect 内帧源先达、m.attach 后注册 querier 的竞态窗，实测窗 ~20s）→
// bpKickRetryDelay 后重入 pendingKick 走同一 drain 径再查，可多次直至 attach 完；
// miss 且非 attaching=真未知 venue → 立即 warn+丢（不静默、不无限重排）。
func (m *BrokerPositionMonitor) requeueKickIfAttaching(kick string) {
	if !m.bm.isAttaching(kick) {
		log.Printf("[broker-mon] kick %s: no matching querier (dropped)", kick)
		return
	}
	time.AfterFunc(bpKickRetryDelay, func() { m.Kick(kick) })
}

// reconcileVenue 单 venue 权威回读（测试与内部直查用）。
func (m *BrokerPositionMonitor) reconcileVenue(ctx context.Context, venueKey string) {
	m.applyVenueResult(ctx, m.bm.venueOrders(ctx, venueKey))
	m.collectSwapSnapshots(ctx)
}

// applyVenueResult REST 权威结果落态：失败 → restOnline=false 保留最近 orders（§2 业主决策）；
// 成功 → 全量替换 + 逐仓 diff 脏化（缺仓 remove 仅此路径可产）。
func (m *BrokerPositionMonitor) applyVenueResult(ctx context.Context, r VenueOrderResult) {
	m.mu.Lock()
	old := m.venues[r.VenueKey]
	if r.Err != nil {
		old.restOnline = false
		m.venues[r.VenueKey] = old // 写回后再算生效在线态（onlineLocked 读 map）
		newOnline := m.onlineLocked(r.VenueKey)
		if old.online != newOnline {
			old.online = newOnline
			for t := range old.orders {
				m.dirtyLocked(posKeyOf(r.VenueKey, t))
			}
		}
		m.venues[r.VenueKey] = old
		m.mu.Unlock()
		return
	}
	old.restOnline = true
	m.venues[r.VenueKey] = old // 写回——onlineLocked/后续读依赖 restOnline 已落态
	m.mu.Unlock()

	// prepareVenueView 查 store/acctMon——锁外执行（D-110 P2 同款纪律）。
	vs := m.prepareVenueView(ctx, r.VenueKey, r.Orders, false)

	m.mu.Lock()
	vs.restOnline = true
	vs.online = m.onlineLocked(r.VenueKey)
	vs.asOf = time.Now() // D-094：权威回读时刻=本 venue 行 asOf（CEX 无帧 patch，回读即事件）
	old = m.venues[r.VenueKey]
	for t := range old.orders {
		if _, ok := vs.orders[t]; !ok {
			m.dirtyLocked(posKeyOf(r.VenueKey, t)) // 权威缺仓 → remove
		}
	}
	// D-571C2 S2：REST 回读不冲帧携现价——同 ticket 新行 PriceAt=0 且旧行有帧价
	// → 沿用旧帧携价（票级保留至下一帧自然覆盖）。
	for t, no := range vs.orders {
		if no.PriceAt.IsZero() {
			if oo, ok := old.orders[t]; ok && !oo.PriceAt.IsZero() {
				no.CurrentPrice, no.PriceAt = oo.CurrentPrice, oo.PriceAt
				vs.orders[t] = no
			}
		}
	}
	for t := range vs.orders {
		m.dirtyLocked(posKeyOf(r.VenueKey, t)) // proto.Equal 在 flush 去重无变化仓
	}
	m.venues[r.VenueKey] = vs
	m.mu.Unlock()
}

// onlineLocked 生效在线态（mu 持锁前提）：流状态已知 → 流权威；缺席 → REST 成败。
func (m *BrokerPositionMonitor) onlineLocked(vk string) bool {
	if m.streamKnown[vk] {
		return m.streamOnline[vk]
	}
	return m.venues[vk].restOnline
}
