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

	"nox/internal/data/venue"
	v1 "nox/internal/gen/dashboardv1"

	"google.golang.org/protobuf/proto"
)

// snapshotVenuesLocked venues 快照（mu 持锁前提）。D-583：orders 发布后可变
// （OnMT5Frame 锁内原地写 vs.orders[ticket]），锁内逐条复制成私有副本随快照带走；
// canon/cs 发布后整体替换即不可变，引用直拷（只拷
// proven-mutable，防过度防御）。
func (m *BrokerPositionMonitor) snapshotVenuesLocked() map[string]venueState {
	venues := make(map[string]venueState, len(m.venues))
	for vk, vs := range m.venues {
		orders := make(map[string]venue.Order, len(vs.orders))
		for t, o := range vs.orders {
			orders[t] = o
		}
		vs.orders = orders
		venues[vk] = vs
	}
	return venues
}

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
	venues := m.snapshotVenuesLocked() // D-583：orders 发布后可变（OnMT5Frame 锁内原地写），深拷防锁外读竞态
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
		changed := !equalIgnoringAsOf(old, u.pb)
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
// D-094：样本行（IsSample）不入 Σ——与拉路径 brokerSums 同口径。
func (m *BrokerPositionMonitor) sums() (pnlSum, netSum float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.cur {
		if p.IsSample {
			continue
		}
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

// equalIgnoringAsOf D-094：行数据 diff 忽略 as_of。asOf 语义=「本行显示值最近一次
// 变更的事件时戳」——值不变不重发（指纹去重语义保持，广播零噪音），asOf 随值变更
// 一起更新；断流后值冻结 → asOf 冻结 + venue_online=false = 陈旧可观测。
func equalIgnoringAsOf(a, b *v1.BrokerPosition) bool {
	if a == nil || b == nil {
		return a == b
	}
	ac, bc := proto.Clone(a).(*v1.BrokerPosition), proto.Clone(b).(*v1.BrokerPosition)
	ac.AsOf, bc.AsOf = nil, nil
	return proto.Equal(ac, bc)
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

// sortedKeys map 键序（确定性输出，回放/快照用）。键支持 string/int64 序型。
func sortedKeys[K ~string | ~int64, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
