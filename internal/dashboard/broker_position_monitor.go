// BrokerPositionMonitor Broker 实持仓视图的单一真相源（D-052）。
// 轮询逐 venue 查询持仓 → 跟踪在线状态（离线保留最近成功快照）→ 变更时经 Topic 广播全量快照。
// 推（StreamUpdates case 8）与拉（ListBrokerPositions）同源；前端全量替换，天然自愈断流。
package dashboard

import (
	"context"
	"log"
	"maps"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/reconcile"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// venueState 单个 venue 的持仓视图状态（在线 + 最近成功快照）。
type venueState struct {
	online bool
	orders []reconcile.VenueOrder
}

// BrokerPositionMonitor 跟踪全部 venue 实际持仓并广播变更。
type BrokerPositionMonitor struct {
	bm       *BrokerManager
	qbus     *bus.QuoteBus
	freshTTL time.Duration
	interval time.Duration
	topic    *bus.Topic[*v1.BrokerPositionSnapshot]

	mu     sync.RWMutex
	venues map[string]venueState // venueKey → 状态
	cache  []*v1.BrokerPosition  // 最近构建的视图（含离线快照）
	lastFP string                // 上次广播指纹（变更检测）
}

// NewBrokerPositionMonitor 创建 monitor。interval 为轮询周期（ARB_BROKER_POS_POLL_MS）。
func NewBrokerPositionMonitor(bm *BrokerManager, qbus *bus.QuoteBus, freshTTL, interval time.Duration, topic *bus.Topic[*v1.BrokerPositionSnapshot]) *BrokerPositionMonitor {
	return &BrokerPositionMonitor{
		bm:       bm,
		qbus:     qbus,
		freshTTL: freshTTL,
		interval: interval,
		topic:    topic,
		venues:   make(map[string]venueState),
	}
}

// Run 轮询直至 ctx 取消。启动即轮询一次（首个快照立即可用）。
func (m *BrokerPositionMonitor) Run(ctx context.Context) {
	m.poll(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

// Refresh 同步执行一次轮询并返回当前视图（前端「刷新」/平仓后立即刷新）。
func (m *BrokerPositionMonitor) Refresh(ctx context.Context) []*v1.BrokerPosition {
	m.poll(ctx)
	return m.Snapshot()
}

// Snapshot 返回当前缓存视图（ListBrokerPositions 拉路径）。
func (m *BrokerPositionMonitor) Snapshot() []*v1.BrokerPosition {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cache
}

// poll 一次轮询：逐 venue 查询 → 更新状态 → 构建视图 → 变更则广播。
func (m *BrokerPositionMonitor) poll(ctx context.Context) {
	results := m.bm.venueOrderResults(ctx)

	m.mu.Lock()
	for _, r := range results {
		vs := m.venues[r.VenueKey]
		if r.Err != nil {
			// 离线：保留最近成功快照（§2 业主决策），不覆盖 orders。
			if !vs.online {
				continue
			}
			vs.online = false
			m.venues[r.VenueKey] = vs
			continue
		}
		m.venues[r.VenueKey] = venueState{online: true, orders: r.Orders}
	}
	m.mu.Unlock()

	view := m.build(ctx)
	fp := fingerprint(view)

	m.mu.Lock()
	m.cache = view
	changed := fp != m.lastFP
	if changed {
		m.lastFP = fp
	}
	m.mu.Unlock()

	if changed {
		if m.topic != nil {
			m.topic.Publish(&v1.BrokerPositionSnapshot{Positions: view})
		}
		log.Printf("[broker-mon] snapshot changed: %d positions", len(view))
	}
}

// build 按 venueKey 排序（确定性）构建全量视图：canonical 解析 + 现价（后端算，D-043）。
// 离线 venue 的持仓仍显示（最近快照），venue_online=false 诚实标注。
func (m *BrokerPositionMonitor) build(ctx context.Context) []*v1.BrokerPosition {
	m.mu.RLock()
	keys := make([]string, 0, len(m.venues))
	for k := range m.venues {
		keys = append(keys, k)
	}
	venues := maps.Clone(m.venues)
	m.mu.RUnlock()
	sort.Strings(keys)

	out := make([]*v1.BrokerPosition, 0, 16)
	symMaps := map[string]map[string]string{} // venue → broker→canonical（懒加载，一次轮询内复用）
	now := time.Now()
	for _, vk := range keys {
		vs := venues[vk]
		for _, o := range vs.orders {
			venue := o.Venue
			canonical := ""
			if cm, ok := symMaps[venue]; ok {
				canonical = cm[o.Symbol]
			} else {
				cm := m.bm.BrokerSymbolMap(ctx, venue)
				symMaps[venue] = cm
				canonical = cm[o.Symbol]
			}
			var price bus.Quote
			hasPrice := false
			if canonical != "" {
				if q, ok := m.qbus.Latest(canonical, o.VenueKey); ok {
					price = q
					hasPrice = true
				}
			}
			out = append(out, brokerPositionToProto(o, canonical, price, hasPrice, vs.online, m.freshTTL, now))
		}
	}
	return out
}

// fingerprint 视图指纹：任一持仓的 key/方向/量/开仓价/浮盈/magic/在线状态变化即触发广播。
// 现价不参与指纹（随 quote 流变化，避免 5s 轮询高频广播；现价由前端 quote 流/快照覆盖）。
func fingerprint(view []*v1.BrokerPosition) string {
	var sb strings.Builder
	for _, p := range view {
		sb.WriteString(p.VenueKey)
		sb.WriteString("|")
		sb.WriteString(p.Ticket)
		sb.WriteString("|")
		sb.WriteString(p.Side)
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(p.Volume, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(p.OpenPrice, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(p.Pnl, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatInt(p.Magic, 10))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatBool(p.VenueOnline))
		sb.WriteString(";")
	}
	return sb.String()
}
