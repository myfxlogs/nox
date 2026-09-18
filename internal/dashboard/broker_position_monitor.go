// BrokerPositionMonitor Broker 实持仓视图的单一真相源（D-052）。
// 轮询逐 venue 查询持仓 → 跟踪在线状态（离线保留最近成功快照）→ 变更时经 Topic 广播全量快照。
// 推（StreamUpdates case 8）与拉（ListBrokerPositions）同源；前端全量替换，天然自愈断流。
package dashboard

import (
	"context"
	"log"
	"maps"
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

	// F5：quote ticker 节流间隔（浮盈实时重算，不查 venue）。
	quoteTick time.Duration

	// D-107 P2：orderTopic 事件触发回读（kickCh buffered(1) 非阻塞 send = 合并语义）。
	kickCh chan struct{}

	// D-110 P2：swap 快照采集（poll 后锁外执行）。
	swapSnapStore    SwapSnapshotStore
	lastSwapByTicket map[string]float64 // ticket→上次 swap 值（去重，单协程访问无需锁）

	// D-124：账户保证金分摊（方案 C）。acctMon nil = 未注入（测试/首 poll），margin=0。
	acctMon *AccountMonitor

	// D-180 S1：结算画像采集器（跳变检测 → 聚合写 swap_settled facts）。
	settlementCollector *SettlementProfileCollector

	// D-337 S2：venue server 时区缓存（swap 倒计时数据源）。nil = 未注入（诚实空态）。
	tzCache *serverTZCache
}

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

// SetAccountMonitor D-124 注入账户 monitor（方案 C 保证金分摊用）。boot 时调用。
func (m *BrokerPositionMonitor) SetAccountMonitor(am *AccountMonitor) {
	m.acctMon = am
}

// SetSettlementProfileCollector D-180 S1：注入结算画像采集器。boot 时调用。
func (m *BrokerPositionMonitor) SetSettlementProfileCollector(c *SettlementProfileCollector) {
	m.settlementCollector = c
}

// SetServerTZCache D-337 S2：注入 venue server 时区缓存（swap 倒计时）。boot 时调用；nil=诚实空态。
func (m *BrokerPositionMonitor) SetServerTZCache(c *serverTZCache) {
	m.tzCache = c
}

// NewBrokerPositionMonitor 创建 monitor。interval 为轮询周期（ARB_BROKER_POS_POLL_MS）。
// F5：quoteTick 为浮盈实时重算节流间隔（默认 1s，不查 venue 只重算浮盈）。
func NewBrokerPositionMonitor(bm *BrokerManager, qbus *bus.QuoteBus, freshTTL, interval, quoteTick time.Duration, topic *bus.Topic[*v1.BrokerPositionSnapshot]) *BrokerPositionMonitor {
	if quoteTick <= 0 {
		quoteTick = time.Second
	}
	return &BrokerPositionMonitor{
		bm:        bm,
		qbus:      qbus,
		freshTTL:  freshTTL,
		interval:  interval,
		topic:     topic,
		venues:    make(map[string]venueState),
		quoteTick: quoteTick,
		kickCh:    make(chan struct{}, 1), // D-107 P2：buffered(1) 非阻塞 send = 事件合并
	}
}

// Kick D-107 P2：orderTopic 事件触发回读（非阻塞 send 到 buffered(1) chan——
// 已有待处理事件即丢=1s 级合并，Run select 消费后触发一次 poll）。
func (m *BrokerPositionMonitor) Kick() {
	select {
	case m.kickCh <- struct{}{}:
	default: // 已有待处理 kick，合并丢弃
	}
}

// Run 轮询直至 ctx 取消。启动即轮询一次（首个快照立即可用）。
// F5：双循环——poll ticker（仓位增减/开平仓，不缩间隔）+ quote ticker（浮盈实时重算，节流 1s）。
// D-107 P2：kickCh 事件触发回读（pollTicker/quoteTicker 保留作兜底）。
func (m *BrokerPositionMonitor) Run(ctx context.Context) {
	m.poll(ctx)
	pollTicker := time.NewTicker(m.interval)
	quoteTicker := time.NewTicker(m.quoteTick) // F5：浮盈节流
	defer pollTicker.Stop()
	defer quoteTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.kickCh:
			m.poll(ctx) // D-107 P2：事件触发回读
		case <-pollTicker.C:
			m.poll(ctx)
		case <-quoteTicker.C:
			m.requote(ctx) // F5：只重算浮盈不查 venue
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

	// D-110 P2：swap 快照采集（锁外执行，D2 修订）。
	m.collectSwapSnapshots(ctx)
}

// collectSwapSnapshots D-110 P2：poll 后采集 swap 快照。
// 锁外执行：复制 venues 快照 → 释放锁 → 构建 positionID 索引 → 遍历 orders 写快照。
// 去重：lastSwapByTicket[ticket] == currentSwap → 跳过；变化或首次 → 写入。
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

// requote F5：quote ticker 节流回调——只重算浮盈（用最新 qbus 报价）不查 venue。
// 仓位增减/开平仓仍走 poll（不缩间隔）；此处仅价格与浮盈跳动。
// 无持仓或无新鲜报价 → 静默跳过（不空广播）。
func (m *BrokerPositionMonitor) requote(ctx context.Context) {
	m.mu.RLock()
	if len(m.venues) == 0 {
		m.mu.RUnlock()
		return
	}
	m.mu.RUnlock()

	view := m.build(ctx)
	fp := fingerprint(view)

	m.mu.Lock()
	m.cache = view
	changed := fp != m.lastFP
	if changed {
		m.lastFP = fp
	}
	m.mu.Unlock()

	if changed && m.topic != nil {
		m.topic.Publish(&v1.BrokerPositionSnapshot{Positions: view})
	}
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
