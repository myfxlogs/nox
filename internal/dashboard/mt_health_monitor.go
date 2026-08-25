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

	mu        sync.Mutex
	states    map[string]mtHealthState // venue → 状态
	firstTick bool                     // 首 tick 只记状态不告警
}

// NewMTHealthMonitor 创建监控（D-104 P0）。
func NewMTHealthMonitor(venues HealthVenueSource, qbus *bus.QuoteBus, store HealthFactStore, tick time.Duration,
	quoteWarn, quoteCrit, factWarn, factCrit time.Duration) *MTHealthMonitor {
	return &MTHealthMonitor{
		venues:    venues,
		qbus:      qbus,
		store:     store,
		tick:      tick,
		quoteWarn: quoteWarn,
		quoteCrit: quoteCrit,
		factWarn:  factWarn,
		factCrit:  factCrit,
		states:    make(map[string]mtHealthState),
		firstTick: true,
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
	now := time.Now()
	quoteAges := m.collectQuoteAges(now)
	swapAges, marginAges := m.collectFactAges(ctx, now)

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
	for _, q := range m.venues.AccountQueriers() {
		v := venueFromKey(q.VenueKey())
		if v != "" {
			venueSet[v] = true
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
		sa := swapAges[v]
		ma := marginAges[v]
		newState := mtHealthState{
			quote:  classifyAge(qa, m.quoteWarn, m.quoteCrit),
			swap:   classifyAge(sa, m.factWarn, m.factCrit),
			margin: classifyAge(ma, m.factWarn, m.factCrit),
		}
		newStates[v] = newState

		// 日志行组件
		logParts = append(logParts, v+"=q:"+durStr(qa)+"/s:"+durStr(sa)+"/m:"+durStr(ma))

		if firstTick {
			continue // 首 tick 只记状态不告警
		}
		prev := prevStates[v]
		m.maybeAlert(ctx, v, "quote", prev.quote, newState.quote, qa)
		m.maybeAlert(ctx, v, "swap", prev.swap, newState.swap, sa)
		m.maybeAlert(ctx, v, "margin", prev.margin, newState.margin, ma)
	}

	m.mu.Lock()
	m.states = newStates
	m.mu.Unlock()

	// 每 tick 一行可观测性日志（WP1 禁静默）
	log.Printf("[mt-health] %s", strings.Join(logParts, " "))
}

// collectQuoteAges 从 qbus 按 Venue 分组取 max(Time)，返回 venue→age。
func (m *MTHealthMonitor) collectQuoteAges(now time.Time) map[string]time.Duration {
	out := make(map[string]time.Duration)
	if m.qbus == nil {
		return out
	}
	latest := make(map[string]time.Time)
	for _, q := range m.qbus.AllLatest() {
		if q.Time.IsZero() {
			continue
		}
		if t, ok := latest[q.Venue]; !ok || q.Time.After(t) {
			latest[q.Venue] = q.Time
		}
	}
	for v, t := range latest {
		out[v] = now.Sub(t)
	}
	return out
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

// classifyAge 按阈值分类三态。age<=0（无数据）→ crit（从未采集=异常）。
func classifyAge(age time.Duration, warn, crit time.Duration) mtHealthMetric {
	if age <= 0 {
		return metricCrit // 无数据=异常
	}
	if age >= crit {
		return metricCrit
	}
	if age >= warn {
		return metricWarn
	}
	return metricHealthy
}

// maybeAlert 状态转换时插告警（healthy→warn/crit 插对应级别；stale→healthy 插 info recovered）。
// 持续同态不重复插（防洪水）。
func (m *MTHealthMonitor) maybeAlert(ctx context.Context, venue, metric string, prev, cur mtHealthMetric, age time.Duration) {
	if prev == cur {
		return // 无转换
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
	if err := m.store.InsertAlert(ctx, nil, level, msg); err != nil {
		log.Printf("[mt-health] alert insert: %v", err)
	}
	log.Printf("[mt-health] %s: %s", level, msg)
}

// venueFromKey 从 "{venue}:{venueType}" 提取 venue 部分。
func venueFromKey(vk string) string {
	if i := strings.IndexByte(vk, ':'); i >= 0 {
		return vk[:i]
	}
	return vk
}

// durStr 时间格式化（<1s 显示秒精度，否则分钟级）。
func durStr(d time.Duration) string {
	if d <= 0 {
		return "n/a"
	}
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}
