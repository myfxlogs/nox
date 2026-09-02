// MTHealthMonitor MT 数据采集稳定性健康监控（D-104 P0）。
// 三指标 per-venue 采集 + 转换式告警状态机（只在状态转换时发告警，防洪水）。
// 指标：①报价新鲜度（qbus.AllLatest 按 Venue 分组取 max(Time)）
//
//	②swap facts 年龄（LatestFacts swap_long/swap_short 按 Venue 取 max(Ts)）
//	③margin facts 年龄（LatestFacts init_margin_rate 按 Venue 取 max(Ts)）
//
// 首个 tick 只记状态不告警（避免重启动噪音）；此后只在转换时插告警。
package dashboard

import (
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
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	ListAccountAliases(ctx context.Context) (map[string]string, error)
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

// MTHealthMonitor MT 数据采集健康监控（D-104 P0）。
type MTHealthMonitor struct {
	venues HealthVenueSource
	qbus   *bus.QuoteBus
	store  HealthFactStore
	tick   time.Duration

	quoteWarn, quoteCrit time.Duration
	factWarn, factCrit   time.Duration

	mu            sync.Mutex
	states        map[string]mtHealthState // venue → 状态
	firstTick     bool                     // 首 tick 只记状态不告警
	lastCritAlert map[string]time.Time     // D-104 P0 返修③ R-c3：venue:metric → 上次 crit 告警时间
	now           func() time.Time         // 可注入时钟（测试用，D-136 修复②），nil→time.Now
}

// NewMTHealthMonitor 创建监控（D-104 P0）。
func NewMTHealthMonitor(venues HealthVenueSource, qbus *bus.QuoteBus, store HealthFactStore, tick time.Duration,
	quoteWarn, quoteCrit, factWarn, factCrit time.Duration) *MTHealthMonitor {
	return &MTHealthMonitor{
		venues:        venues,
		qbus:          qbus,
		store:         store,
		tick:          tick,
		quoteWarn:     quoteWarn,
		quoteCrit:     quoteCrit,
		factWarn:      factWarn,
		factCrit:      factCrit,
		states:        make(map[string]mtHealthState),
		firstTick:     true,
		lastCritAlert: make(map[string]time.Time),
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

	// venue 列表 = brokerMgr venues ∪ quotes venues ∪ facts venues（全覆盖）
	venueSet := make(map[string]bool)
	for v := range quoteAges {
		venueSet[v] = true
	}
	for v := range swapAges {
		venueSet[v] = true
	}
	for v := range marginAges {
		venueSet[v] = true
	}
	// D-104 R-d1：swap/margin 仅 MT venue（CEX 无 facts）。D-203：margin 仅 MT5 venue
	// （mt_margin_source 只挂 MT5Adapter，MT4 无 init_margin_rate → 永久 n/a 假洪水）。
	mtSet := make(map[string]bool)
	mt5Set := make(map[string]bool)
	for _, q := range m.venues.AccountQueriers() {
		vk := q.VenueKey()
		v := venueFromKey(vk)
		if v != "" {
			venueSet[v] = true
			mtSet[v] = true
			if strings.HasSuffix(vk, ":mt5") {
				mt5Set[v] = true
			}
		}
	}

	m.mu.Lock()
	firstTick := m.firstTick
	m.firstTick = false
	prevStates := m.states
	newStates := make(map[string]mtHealthState, len(venueSet))
	m.mu.Unlock()

	var logParts []string
	for v := range venueSet {
		qa := quoteAges[v]
		isMT := mtSet[v]
		isMT5 := mt5Set[v]
		// quote 全 venue；swap 仅 MT；margin 仅 MT5（D-203：MT4 无 margin facts）。
		newState := mtHealthState{
			quote:  classifyAge(qa, m.quoteWarn, m.quoteCrit),
			swap:   metricHealthy,
			margin: metricHealthy,
		}
		sLog, mLog := "-", "-"
		if isMT {
			sa := swapAges[v]
			newState.swap = classifyAge(sa, m.factWarn, m.factCrit)
			sLog = durStr(sa)
		}
		if isMT5 {
			ma := marginAges[v]
			newState.margin = classifyAge(ma, m.factWarn, m.factCrit)
			mLog = durStr(ma)
		}
		newStates[v] = newState

		// 日志行组件（用别名，回退裸账号；非 MT venue 的 s:/m: 显示 "-"）
		display := aliasOrVenue(v, aliases)
		logParts = append(logParts, display+"=q:"+durStr(qa)+"/s:"+sLog+"/m:"+mLog)

		if firstTick {
			continue // 首 tick 只记状态不告警
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
	// swap：long + short 合并取 max
	swapLongs, err := m.store.LatestFacts(ctx, fact.KindSwapLong, "", "")
	if err != nil {
		log.Printf("[mt-health] swap_long facts: %v", err)
	}
	swapShorts, err := m.store.LatestFacts(ctx, fact.KindSwapShort, "", "")
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
	margins, err := m.store.LatestFacts(ctx, fact.KindInitMarginRate, "", "")
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

// critRealertInterval crit 持续同态重告警去重窗口（D-104 P0 返修③ R-c3）。
// 持续 crit 每 30min 重发一条（有界，不洪水）；堵 boot 首 tick 静默边角——
// boot 时真缺失首 tick 静默记 crit，tick2（1min 后）转换式告警触发，之后持续 crit 每 30min 一条。
const critRealertInterval = 30 * time.Minute

// maybeAlert 状态转换时插告警（healthy→warn/crit；stale→healthy 插 info recovered）。
// 持续同态不重复插（防洪水）——crit 持续 >30min 重发一条（R-c3）；D-136 修复②：周末休市静默窗内告警静默，状态机照常运转。
func (m *MTHealthMonitor) maybeAlert(ctx context.Context, venue, metric string, prev, cur mtHealthMetric, age time.Duration) {
	if isMTWeekendQuietWindow(m.nowTime()) {
		return // 周末休市静默窗
	}
	if prev == cur {
		// R-c3：持续 crit 超过去重窗口 → 重发一条（warn/healthy 维持纯转换语义不变）。
		if cur != metricCrit {
			return
		}
		key := venue + ":" + metric
		m.mu.Lock()
		last, ok := m.lastCritAlert[key]
		m.mu.Unlock()
		if ok && m.nowTime().Sub(last) < critRealertInterval {
			return // 窗口内不重发
		}
	}
	var level, msg string
	switch cur {
	case metricHealthy:
		level = "info"
		msg = "mt-health " + venue + " " + metric + " recovered (age=" + durStr(age) + ")"
	case metricWarn:
		level = "warn"
		msg = "mt-health " + venue + " " + metric + " stale (age=" + durStr(age) + ")"
	case metricCrit:
		level = "critical"
		msg = "mt-health " + venue + " " + metric + " critical (age=" + durStr(age) + ")"
	}
	if cur == metricCrit {
		key := venue + ":" + metric
		m.mu.Lock()
		m.lastCritAlert[key] = m.nowTime()
		m.mu.Unlock()
	}
	if err := m.store.InsertAlert(ctx, nil, level, msg); err != nil {
		log.Printf("[mt-health] alert insert: %v", err)
	}
	log.Printf("[mt-health] %s: %s", level, msg)
}
