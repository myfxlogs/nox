// broker_position_events.go D-571 包A：逐仓事件发射面（dirty 合批 flush + 回放）。
// 发射纪律：flush 时按 posKey 最新态裁决 upsert/remove（dirty 集合不记 kind，
// 帧到达与发射之间的状态回摆自然归零）；upsert 过 proto.Equal 指纹去重；
// remove 仅当 cur 中无该仓态（权威缺仓由 applyVenueResult 脏化；帧外仓不脏化）。
package dashboard

import (
	"context"
	"log"
	"sort"
	"time"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/keydomain"

	"google.golang.org/protobuf/proto"
)

// flush 尾沿合批发射：取走 dirty 集 → 逐 key 按最新态裁决 → 聚合值随每个事件顺带。
// 锁纪律：dirty/venues/cur 快照在锁内取，protoFor（查 store/qbus/facts）在锁外，
// 发射后回锁写 cur（期间旧态被新脏化覆盖不丢——dirty 已清空，下一轮再发）。
func (m *BrokerPositionMonitor) flush(ctx context.Context) {
	m.mu.Lock()
	m.flushT = nil
	if len(m.dirty) == 0 {
		m.mu.Unlock()
		return
	}
	keys := make([]string, 0, len(m.dirty))
	for k := range m.dirty {
		keys = append(keys, k)
	}
	m.dirty = make(map[string]struct{})
	venues := make(map[string]venueState, len(m.venues))
	for vk, vs := range m.venues {
		venues[vk] = vs
	}
	m.mu.Unlock()
	sort.Strings(keys) // 确定性发射序

	now := time.Now()
	// 先构建/判定删除，upsert 集批量标注（每 flush 一次索引，勿逐仓查库）。
	type cand struct {
		key string
		pb  *v1.BrokerPosition
	}
	var upserts []cand
	var removed []string
	for _, key := range keys {
		vk, ticket := splitPosKey(key)
		vs, ok := venues[vk]
		o, alive := vs.orders[ticket]
		if !ok || !alive {
			removed = append(removed, key)
			continue
		}
		upserts = append(upserts, cand{key: key, pb: m.protoFor(ctx, vk, vs, o, now)})
	}
	if m.annotateFn != nil && len(upserts) > 0 {
		bps := make([]*v1.BrokerPosition, 0, len(upserts))
		for _, u := range upserts {
			bps = append(bps, u.pb)
		}
		m.annotateFn(ctx, bps)
	}
	var events []*v1.BrokerPositionEvent
	for _, key := range removed {
		vk, ticket := splitPosKey(key)
		// 仓已不在权威集 → remove（仅当 cur 中有旧态才需发；无则本就不可见）。
		m.mu.Lock()
		_, wasPublished := m.cur[key]
		delete(m.cur, key)
		m.mu.Unlock()
		if wasPublished {
			events = append(events, &v1.BrokerPositionEvent{
				Kind: &v1.BrokerPositionEvent_Remove{
					Remove: &v1.BrokerPositionRef{VenueKey: vk, Ticket: ticket},
				},
			})
		}
	}
	for _, u := range upserts {
		m.mu.Lock()
		old := m.cur[u.key]
		changed := !proto.Equal(old, u.pb)
		if changed {
			m.cur[u.key] = u.pb
		}
		m.mu.Unlock()
		if changed {
			events = append(events, &v1.BrokerPositionEvent{
				Kind: &v1.BrokerPositionEvent_Upsert{Upsert: u.pb},
			})
		}
	}
	if len(events) == 0 {
		return
	}
	pnlSum, netSum := m.sums()
	for _, ev := range events {
		ev.PnlSumUsd = pnlSum
		ev.NetSumUsd = netSum
	}
	if m.evTopic != nil {
		for _, ev := range events {
			m.evTopic.Publish(ev)
		}
	}
	log.Printf("[broker-mon] events flushed: %d", len(events))
}

// sums 当前视图聚合（Σ pnl / Σ(pnl+swap+commission)；brokerSums 同口径，
// 全零/空仓 → 0——事件字段为标量 double 无 nil 缺省态，空态语义由前端条显区分）。
func (m *BrokerPositionMonitor) sums() (pnlSum, netSum float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.cur {
		pnlSum += p.Pnl
		netSum += p.Pnl + p.Swap + p.Commission
	}
	return pnlSum, netSum
}

// Events 回放源：cur 全量 → 每仓一条 upsert 事件（新订阅初始化态，service_stream_init 用）。
// 聚合值顺带（与 flush 路径同口径）。
func (m *BrokerPositionMonitor) Events() []*v1.BrokerPositionEvent {
	m.mu.Lock()
	out := make([]*v1.BrokerPositionEvent, 0, len(m.cur))
	for _, k := range sortedKeys(m.cur) {
		out = append(out, &v1.BrokerPositionEvent{
			Kind: &v1.BrokerPositionEvent_Upsert{Upsert: m.cur[k]},
		})
	}
	m.mu.Unlock()
	pnlSum, netSum := m.sums()
	for _, ev := range out {
		ev.PnlSumUsd = pnlSum
		ev.NetSumUsd = netSum
	}
	return out
}

// splitPosKey posKey → (venueKey, ticket)。venueKey 含 ":" 无 "|"，ticket 同理——split 安全。
func splitPosKey(key string) (venueKey, ticket string) {
	for i := 0; i < len(key); i++ {
		if key[i] == '|' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// sortedKeys map 键序（确定性输出，回放/快照用）。
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

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
		log.Printf("[broker-mon] kick %s: no matching querier (ignored)", kick)
		return
	}
	m.collectSwapSnapshots(ctx)
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
	old = m.venues[r.VenueKey]
	for t := range old.orders {
		if _, ok := vs.orders[t]; !ok {
			m.dirtyLocked(posKeyOf(r.VenueKey, t)) // 权威缺仓 → remove
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
