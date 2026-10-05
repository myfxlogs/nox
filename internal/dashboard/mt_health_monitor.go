// MTHealthMonitor MT 数据采集稳定性健康监控（D-104 P0）。
// 三指标 per-venue 采集 + 转换式告警状态机（状态转换时发告警，防洪水）。
// 指标：①报价新鲜度 ②swap facts 年龄 ③margin facts 年龄（D-203 仅 MT5）。
// boot 宽限：前 bootGraceTicks 个 tick 只记状态不告警（给采集器启动时间，防重启噪音）。
package dashboard

import (
	"arb/internal/keydomain"
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
	"arb/internal/reconcile"
)

// HealthFactStore 健康监控所需的 fact 查询接口（pgstore.Store 满足）。
type HealthFactStore interface {
	LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error)
	LatestFactsByKind(ctx context.Context, kind string) ([]fact.Fact, error) // D-508 S3：kind-only 无 OR 专用查询
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	ListAccountAliases(ctx context.Context) (map[string]string, error)
	ListBrokerAccountStatuses(ctx context.Context) (map[string]string, error) // D-216：venue→status，收紧 venueSet
}

// HealthVenueSource 提供 MT venue 列表（BrokerManager 满足）。
type HealthVenueSource interface {
	AccountQueriers() []reconcile.AccountQuerier
}

// mtHealthMetric 单个指标的三态。
type mtHealthMetric int

const (
	metricHealthy mtHealthMetric = iota
	metricWarn
	metricCrit
)

// mtHealthState per-venue+metric 状态条目。
type mtHealthState struct {
	quote  mtHealthMetric
	swap   mtHealthMetric
	margin mtHealthMetric
}

type MTHealthMonitor struct {
	venues HealthVenueSource
	qbus   *bus.QuoteBus
	store  HealthFactStore
	tick   time.Duration

	quoteWarn, quoteCrit   time.Duration
	factWarn, factCrit     time.Duration
	marginWarn, marginCrit time.Duration // D-067：margin（init_margin_rate 准静态、重连驱动写入）独立阈值

	mu              sync.Mutex
	states          map[string]mtHealthState // venue → 状态
	bootGraceTicks  int                      // boot 宽限剩余 tick 数（>0 时只记状态不告警）
	lastCritAlert   map[string]time.Time     // D-104 P0 返修③ R-c3：venue:metric → 上次 crit 告警时间
	marginMissCount map[string]int           // D-251：venue → margin 连续缺失计数（≥3 才 crit）
	quoteMissCount  map[string]int           // R-H4：venue → quote 连续缺失计数（≥3 才 crit，与 margin 共用 helper）
	now             func() time.Time         // 可注入时钟（测试用，D-136 修复②），nil→time.Now
}

// NewMTHealthMonitor 创建监控（D-104 P0）。
func NewMTHealthMonitor(venues HealthVenueSource, qbus *bus.QuoteBus, store HealthFactStore, tick time.Duration,
	quoteWarn, quoteCrit, factWarn, factCrit, marginWarn, marginCrit time.Duration) *MTHealthMonitor {
	return &MTHealthMonitor{
		venues:          venues,
		qbus:            qbus,
		store:           store,
		tick:            tick,
		quoteWarn:       quoteWarn,
		quoteCrit:       quoteCrit,
		factWarn:        factWarn,
		factCrit:        factCrit,
		marginWarn:      marginWarn,
		marginCrit:      marginCrit,
		states:          make(map[string]mtHealthState),
		bootGraceTicks:  3, // boot 宽限 3 tick（≈3min）给采集器启动，防重启 critical 噪音
		lastCritAlert:   make(map[string]time.Time),
		marginMissCount: make(map[string]int),
		quoteMissCount:  make(map[string]int),
	}
}

// Run 轮询直至 ctx 取消（模式同 AccountMonitor.Run）。
func (m *MTHealthMonitor) Run(ctx context.Context) {
	m.poll(ctx)
	ticker := time.NewTicker(m.tick)
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

// poll 一次采集 + 状态机转换 + 告警 + 日志。
func (m *MTHealthMonitor) poll(ctx context.Context) {
	now := m.nowTime()
	quoteAges := m.collectQuoteAges(now)
	swapAges, marginAges := m.collectFactAges(ctx, now)
	aliases := m.collectAliases(ctx) // D-203：告警/日志用别名（回退裸账号）

	// D-216 venueSet 收紧：mtSet ∪ connectedSet ∪ (qbusVenues \ knownSet)；status 来自 broker_accounts（窄查询零凭据），err 降级=空集。走查表见 D-216 提示词 §3.S2。
	statuses, err := m.store.ListBrokerAccountStatuses(ctx)
	knownSet := make(map[string]bool)
	connectedSet := make(map[string]bool)
	if err != nil {
		log.Printf("[mt-health] broker statuses: %v", err)
	} else {
		for v, st := range statuses {
			knownSet[v] = true
			if st == "connected" {
				connectedSet[v] = true
			}
		}
	}
	venueSet := make(map[string]bool)
	mtSet := make(map[string]bool)  // swap 仅 MT venue（D-104 R-d1）
	mt5Set := make(map[string]bool) // margin 仅 MT5 venue（D-203）
	for _, q := range m.venues.AccountQueriers() {
		vk := q.VenueKey()
		v := venueFromKey(vk)
		if v != "" {
			venueSet[v] = true
			mtSet[v] = true
			if keydomain.IsType(vk, "mt5") {
				mt5Set[v] = true
			}
		}
	}
	for v := range connectedSet { // connected 即使无数据也监控（D-205 型真告警）
		venueSet[v] = true
	}
	for v := range quoteAges { // qbusVenues \ knownSet：CEX 等不在 broker_accounts 的 venue 兜底
		if !knownSet[v] {
			venueSet[v] = true
		}
	}
	// D-400：stopped 账户排除出监控集——static querier 不随 DisableBroker 注销（broker_manager
	// 只清 running/reg），径①收录的停用账户上游无报价永远 critical（ic52993526/ICR15032313 实证）。
	// 放 venueSet 成型后统一过滤=三路收录径全盖（含未来新增径）；statuses err→nil map 零命中不排除
	// （fail-open，监控集行为不变）；failed 不排（应连未连=告警语义正确），只排 stopped。
	for v := range venueSet {
		if statuses[v] == "stopped" {
			delete(venueSet, v)
		}
	}

	m.mu.Lock()
	inGrace := m.bootGraceTicks > 0
	if inGrace {
		m.bootGraceTicks--
	}
	prevStates := m.states
	newStates := make(map[string]mtHealthState, len(venueSet))
	m.mu.Unlock()

	var logParts []string
	for v := range venueSet {
		qa := quoteAges[v]
		isMT := mtSet[v]
		isMT5 := mt5Set[v]
		display := aliasOrVenue(v, aliases) // D-203：告警/日志用别名（回退裸账号）
		// quote 全 venue（R-H4：flash-miss 治理同 margin）；swap 仅 MT；margin 仅 MT5（D-203：MT4 无 margin facts）。
		qAdjusted, _, qSuppressed := m.classifyWithFlashMiss(v, classifyAge(qa, m.quoteWarn, m.quoteCrit), m.quoteMissCount)
		newState := mtHealthState{
			quote:  qAdjusted,
			swap:   metricHealthy,
			margin: metricHealthy,
		}
		sLog, mLog := "-", "-"
		if qSuppressed {
			log.Printf("[mt-health] %s quote flash-miss suppressed (age=%s)", display, durStr(qa))
		}
		if isMT {
			sa := swapAges[v]
			newState.swap = classifyAge(sa, m.factWarn, m.factCrit)
			sLog = durStr(sa)
		}
		if isMT5 {
			ma := marginAges[v]
			// D-067：margin 用独立阈值（init_margin_rate 重连驱动写入，流稳时不写为设计内）。
			adjusted, _, suppressed := m.classifyWithFlashMiss(v, classifyAge(ma, m.marginWarn, m.marginCrit), m.marginMissCount)
			newState.margin = adjusted
			mLog = durStr(ma)
			if suppressed {
				log.Printf("[mt-health] %s margin flash-miss suppressed (age=%s)", display, mLog)
			}
		}
		newStates[v] = newState
		logParts = append(logParts, display+"=q:"+durStr(qa)+"/s:"+sLog+"/m:"+mLog)

		if inGrace {
			continue // boot 宽限内只记状态不告警
		}
		prev := prevStates[v]
		m.maybeAlert(ctx, display, "quote", prev.quote, newState.quote, qa)
		if isMT {
			m.maybeAlert(ctx, display, "swap", prev.swap, newState.swap, swapAges[v])
		}
		if isMT5 {
			m.maybeAlert(ctx, display, "margin", prev.margin, newState.margin, marginAges[v])
		}
	}

	m.mu.Lock()
	m.states = newStates
	m.mu.Unlock()

	// 每 tick 一行可观测性日志（WP1 禁静默）
	log.Printf("[mt-health] %s", strings.Join(logParts, " "))
}

// collectFactAges 从 store 查 swap/margin facts，按 Venue 分组取 max(Ts)，返回 venue→age。
func (m *MTHealthMonitor) collectFactAges(ctx context.Context, now time.Time) (swapAges, marginAges map[string]time.Duration) {
	swapAges = make(map[string]time.Duration)
	marginAges = make(map[string]time.Duration)
	if m.store == nil {
		return
	}
	swapLongs, err := m.store.LatestFactsByKind(ctx, fact.KindSwapLong)
	if err != nil {
		log.Printf("[mt-health] swap_long facts: %v", err)
	}
	swapShorts, err := m.store.LatestFactsByKind(ctx, fact.KindSwapShort)
	if err != nil {
		log.Printf("[mt-health] swap_short facts: %v", err)
	}
	swapLatest := make(map[string]time.Time)
	for _, f := range swapLongs {
		if f.Ts.IsZero() {
			continue
		}
		if t, ok := swapLatest[f.Venue]; !ok || f.Ts.After(t) {
			swapLatest[f.Venue] = f.Ts
		}
	}
	for _, f := range swapShorts {
		if f.Ts.IsZero() {
			continue
		}
		if t, ok := swapLatest[f.Venue]; !ok || f.Ts.After(t) {
			swapLatest[f.Venue] = f.Ts
		}
	}
	for v, t := range swapLatest {
		swapAges[v] = now.Sub(t)
	}
	// margin
	margins, err := m.store.LatestFactsByKind(ctx, fact.KindInitMarginRate)
	if err != nil {
		log.Printf("[mt-health] margin facts: %v", err)
	}
	marginLatest := make(map[string]time.Time)
	for _, f := range margins {
		if f.Ts.IsZero() {
			continue
		}
		if t, ok := marginLatest[f.Venue]; !ok || f.Ts.After(t) {
			marginLatest[f.Venue] = f.Ts
		}
	}
	for v, t := range marginLatest {
		marginAges[v] = now.Sub(t)
	}
	return
}
