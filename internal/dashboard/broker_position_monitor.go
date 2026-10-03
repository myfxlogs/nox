// BrokerPositionMonitor Broker 实持仓视图单一真相源（D-052 起；D-571 包A 事件化重写）：
// 逐仓事件替代周期全量快照——MT5 OnOrderProfit 帧 patch / CEX 持仓事件 kick / 订单事件 kick /
// qbus 报价驱动浮盈联动 / 流状态事件承担 venue_online；REST 只在权威点回读（boot/kick/未知票号）。
// 推（StreamUpdates case 15）与拉（ListBrokerPositions）同源；新订阅回放 cur 全量，前端 keyed merge。
package dashboard

import (
	"context"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/collect/exchange"
	"arb/internal/keydomain"
	"arb/internal/venue"

	v1 "arb/internal/gen/dashboardv1"
)

// venueState 单个 venue 的持仓视图状态（在线 + 持仓集 + 显示缓存）。
// orders 以 ticket 为键（D-571 包A 逐仓化）；canon/cs 在 REST 重读时重建缓存，
// 帧 patch 沿用（canonical 低频变化）。
type venueState struct {
	online     bool                   // 生效在线态（流状态已知则取流，否则取 REST 成败）
	restOnline bool                   // REST 查询成败（流状态缺席时的在线判定源）
	orders     map[string]venue.Order // ticket → order
	canon      map[string]string      // symbol→canonical（reconcile 重建）
	cs         map[string]float64     // symbol→contractSize（reconcile 重建）
}

// posKey 逐仓键：venueKey + "|" + ticket。
func posKeyOf(venueKey, ticket string) string { return venueKey + "|" + ticket }

// BrokerPositionMonitor 跟踪全部 venue 实际持仓并以逐仓事件广播变更。
type BrokerPositionMonitor struct {
	bm       *BrokerManager
	qbus     *bus.QuoteBus
	freshTTL time.Duration
	evTopic  *bus.Topic[*v1.BrokerPositionEvent]

	mu     sync.RWMutex
	venues map[string]venueState // venueKey → 状态
	// d031 D-031 ②/R2：MT5 仓位级保证金域（缓存/退避/imr——broker_position_monitor_margin.go）。
	d031   d031MarginState
	cur    map[string]*v1.BrokerPosition // posKey → 已发布显示态（回放源+proto.Equal 去重）
	dirty  map[string]struct{}           // 待发事件 posKey 集（flush 时按最新态裁决 upsert/remove）
	flushT *time.Timer                   // 尾沿合批定时器（AfterFunc，有界非周期）
	// D-571 包A：venue_online 由流状态事件承担——流已知则权威，缺席回退 REST 成败。
	streamKnown  map[string]bool
	streamOnline map[string]bool

	// kick：事件触发按 venue 权威回读（pendingKick 集合去重 + kickSig cap1 信号）。
	pendingKick map[string]struct{}
	kickSig     chan struct{}

	// D-110 P2：swap 快照采集（REST 重读后锁外执行）。
	swapSnapStore    SwapSnapshotStore
	lastSwapByTicket map[string]float64 // ticket→上次 swap 值（去重，单协程访问无需锁）

	// D-124→D-008：账户保证金分摊已撤（venue 无仓位级 margin 字段=诚实空态）。
	// acctMon 保留字段已删——无其他消费面。

	// D-180 S1：结算画像采集器（跳变检测 → 聚合写 swap_settled facts）。
	settlementCollector *SettlementProfileCollector

	// D-571 包A：事件载荷标注器（与 case 8 同源——position_id/order_id/alias/CEX swap
	// 由 Service 经 store 注入；main 装配同 commit）。nil=不标注（测试）。
	annotateFn func(ctx context.Context, bps []*v1.BrokerPosition)

	// D-337R2/R3/R4：tzCache/cexIntervals/cexFundingRate——构造必选/同 commit 接线，nil/零值=诚实空态。
	tzCache        ServerTZCache
	cexIntervals   *exchange.FundingIntervalCache
	cexFundingRate func(ctx context.Context, venue, symbol string) (annualPct float64, ok bool)
	// D-466 S3：腿级 venue 原生结算时刻/周期源（funding_next_settle+funding_interval
	// facts）。返回零值=该腿无真值 fact（诚实缺→interval 边界→nil 空态+warn，D-466R1 禁 8h 兜底）。
	cexNextSettle func(ctx context.Context, venue, symbol string) (nextSettle, factTs time.Time, interval time.Duration)
}

// bpFlushDelay 逐仓事件尾沿合批窗口（AfterFunc 有界单次，非周期定时器）。
// 包级 var 便于测试缩短；同 posKey 窗口内多次脏化只发最新态（合并语义）。
var bpFlushDelay = 200 * time.Millisecond

// SetCEXFundingRate D-337R3 S1：注入 CEX 腿年化费率源（构造必选第 8 参之外的同 commit 接线面）。
func (m *BrokerPositionMonitor) SetCEXFundingRate(fundingRate func(ctx context.Context, venue, symbol string) (float64, bool)) {
	m.cexFundingRate = fundingRate
}

// SetSettlementProfileCollector D-180 S1：注入结算画像采集器。boot 时调用。
func (m *BrokerPositionMonitor) SetSettlementProfileCollector(c *SettlementProfileCollector) {
	m.settlementCollector = c
}

// SetAnnotator D-571 包A：注入事件载荷标注器（Service 侧 annotatePositionID/
// annotateAccountAlias/annotateCEXSwap 三件套闭包——与 case 8 流推送同源口径）。
func (m *BrokerPositionMonitor) SetAnnotator(f func(ctx context.Context, bps []*v1.BrokerPosition)) {
	m.annotateFn = f
}

// NewBrokerPositionMonitor 创建 monitor（D-571 包A：poll/quote 定时器已移除，事件驱动）。
// D-337R2 S1：tzCache 构造必选注入（忘接=编译失败；零值 ServerTZCache=诚实空态）。
// D-337R3 S1：cexIntervals 构造必选第 8 参（CEX 腿 funding 结算周期；忘接=编译失败，
// nil=CEX 腿诚实空态）——house rule：新依赖一律构造必选禁新增 Set*；拆分/新增任何
// Set* 必须同 commit 接线并全树 grep 自证零遗漏（D-337R4 S2 补强）。
// （setter 可忘=装配断链，D-337R1/R2/R3 三连教训；既有 Set* 仅存量兼容）。
func NewBrokerPositionMonitor(bm *BrokerManager, qbus *bus.QuoteBus, freshTTL time.Duration, topic *bus.Topic[*v1.BrokerPositionEvent], tzCache ServerTZCache, cexIntervals *exchange.FundingIntervalCache, cexNextSettle func(ctx context.Context, venue, symbol string) (time.Time, time.Time, time.Duration)) *BrokerPositionMonitor {
	return &BrokerPositionMonitor{
		bm:            bm,
		tzCache:       tzCache,
		cexIntervals:  cexIntervals,
		cexNextSettle: cexNextSettle,
		qbus:          qbus,
		freshTTL:      freshTTL,
		evTopic:       topic,
		venues:        make(map[string]venueState),
		cur:           make(map[string]*v1.BrokerPosition),
		dirty:         make(map[string]struct{}),
		streamKnown:   make(map[string]bool),
		streamOnline:  make(map[string]bool),
		pendingKick:   make(map[string]struct{}),
		kickSig:       make(chan struct{}, 1),
	}
}

// Kick 事件触发按 venue 权威回读（D-107 P2 起，D-571 改为 venue 定向）。
// venueKey 完整键（"acct:mt5"/"binance:perp"）或裸 venue 名（"acct" → 该 venue 全部类型键）。
// 集合去重 + cap1 信号 = 事件合并（连发同 venue 至多一轮回读）。空串 = 全 venue。
func (m *BrokerPositionMonitor) Kick(venueKey string) {
	m.mu.Lock()
	m.pendingKick[venueKey] = struct{}{}
	m.mu.Unlock()
	select {
	case m.kickSig <- struct{}{}:
	default: // 已有待处理信号，合并丢弃
	}
}

// OnMT5Frame D-571 包A：MT5 OnOrderProfit 帧逐仓 patch（PositionsSink 入口）。
// 语义：帧内已知票号更新字段；未知票号不建仓、触发该 venue 权威回读；
// 帧外仓（帧未载）不动不删——缺仓只 REST 权威裁决。
func (m *BrokerPositionMonitor) OnMT5Frame(venueKey string, orders []venue.Order) {
	unknown := false
	m.mu.Lock()
	vs := m.venues[venueKey]
	for _, o := range orders {
		if _, ok := vs.orders[o.Ticket]; !ok {
			unknown = true // 未知票号 → 帧外权威裁决（REST 回读），不就地建仓
			continue
		}
		vs.orders[o.Ticket] = o
		m.dirtyLocked(posKeyOf(venueKey, o.Ticket))
	}
	m.venues[venueKey] = vs
	m.mu.Unlock()
	if unknown {
		m.Kick(venueKey)
	}
}

// OnStreamState D-571 包A：venue 流状态事件（StreamStateSink 入口）。
// venue_online 由流承担：状态翻转 → 该 venue 全部持仓脏化重发（VenueOnline 字段变化）。
func (m *BrokerPositionMonitor) OnStreamState(venueKey string, online bool) {
	m.mu.Lock()
	m.streamKnown[venueKey] = true
	m.streamOnline[venueKey] = online
	vs := m.venues[venueKey]
	if vs.online == online {
		m.mu.Unlock()
		return // 生效在线态未翻转（含首报 false 而 REST 已判离线等幂等情形）
	}
	vs.online = online
	for t := range vs.orders {
		m.dirtyLocked(posKeyOf(venueKey, t))
	}
	m.venues[venueKey] = vs
	m.mu.Unlock()
}

// StreamOnlineSnapshot D-571C-R2 F-1：流权威 venue 在线态快照（sourceHealth
// 组装读口——StreamStateSink 事件先落本表，assembleSourceHealth 须编入否则
// 状态翻转踢了 posMon 也无 diff=死踢）。只读拷贝。
func (m *BrokerPositionMonitor) StreamOnlineSnapshot() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]bool, len(m.streamOnline))
	for vk, on := range m.streamOnline {
		out[vk] = on
	}
	return out
}

// onQuote qbus 通配订阅回调：报价到达 → 匹配持仓脏化（flush 时重取 LatestLeg 重算现价）。
// 匹配镜像 LatestLeg 键序：CEX 原生归一键优先 + canonical 兜底 + 原生 brokerSym 命中
// （pb.Symbol==q.VenueSymbol，D-571C2 S3：降格原生键发布的变体 tick 仍脏化对应行）；
// MT5 行 S4 收束——现价帧内携，quote 脏化恒无 diff=纯空踢（MT4 无帧流仍靠拼接，不动）。
func (m *BrokerPositionMonitor) onQuote(q bus.Quote) {
	vk := q.VenueKey()
	if keydomain.IsType(vk, "mt5") {
		return // S4：mt5 行价只认帧携——quote 死踢收束
	}
	cex := bus.IsCexVenueKey(vk)
	m.mu.Lock()
	for key, pb := range m.cur {
		if pb.VenueKey != vk {
			continue
		}
		if pb.Canonical == q.Symbol || pb.Symbol == q.VenueSymbol || (cex && bus.CexSymbolKey(pb.Symbol) == q.Symbol) {
			m.dirtyLocked(key)
		}
	}
	m.mu.Unlock()
}

// Run 事件循环直至 ctx 取消。启动即全量权威回读一次（首个视图立即可用）。
// D-571 包A：无周期定时器——kick（订单/持仓事件）、quote（qbus 通配订阅）双源驱动。
func (m *BrokerPositionMonitor) Run(ctx context.Context) {
	quoteCh, cancel := m.qbus.Subscribe("") // 通配订阅=全 symbol 报价帧
	defer cancel()
	m.reconcileAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.kickSig:
			for _, vk := range m.drainKicks() {
				if vk == "" {
					m.reconcileAll(ctx)
				} else {
					m.reconcileVenueKeys(ctx, vk)
				}
			}
		case q, ok := <-quoteCh:
			if !ok {
				return
			}
			m.onQuote(q)
		}
	}
}

// Refresh 同步执行一次全量权威回读 + flush 并返回当前视图（前端「刷新」/平仓后立即刷新）。
func (m *BrokerPositionMonitor) Refresh(ctx context.Context) []*v1.BrokerPosition {
	m.reconcileAll(ctx)
	m.flushBounded(ctx) // D-575 F-3：直调 flush 同包 20s 界灭最后无界调用方
	return m.Snapshot()
}

// Snapshot 返回当前缓存视图（ListBrokerPositions 拉路径；cur 已发布态按键序排列）。
func (m *BrokerPositionMonitor) Snapshot() []*v1.BrokerPosition {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

// snapshotLocked cur → 有序切片（mu 持锁前提；仅读引用不克隆——proto 发布后不可变约束）。
func (m *BrokerPositionMonitor) snapshotLocked() []*v1.BrokerPosition {
	out := make([]*v1.BrokerPosition, 0, len(m.cur))
	for _, k := range sortedKeys(m.cur) {
		out = append(out, m.cur[k])
	}
	return out
}

// dirtyLocked 脏化 posKey 并挂尾沿 flush（mu 持锁前提；同 key 窗口内合并为最新态）。
func (m *BrokerPositionMonitor) dirtyLocked(posKey string) {
	m.dirty[posKey] = struct{}{}
	if m.flushT == nil {
		bound := bpFlushBound // D-575：调度时刻同步取值——闭包不再异步读包级 var（防测试覆写竞态）
		m.flushT = time.AfterFunc(bpFlushDelay, func() {
			// D-575：flush 整体 20s 界——超时各内部调用诚实失败，事件照发不误。
			ctx, cancel := context.WithTimeout(context.Background(), bound)
			defer cancel()
			m.flush(ctx)
		})
	}
}
