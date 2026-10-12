// TableFreshnessMonitor 通用表级新鲜度监控（D-301a S1）。
// 照抄 mt_health_monitor.go 的三态转换式告警状态机（healthy→warn→crit 转换时发，
// 同态不重复；crit 持续 30min 重发一条；bootGraceTicks=3；可注入时钟）。
// 白名单驱动——TableWatch.Name 对应 pgstore 侧常量 SQL map，表名/列名不入参数化，
// 无 SQL 注入面。不加周末静默窗（表新鲜度与休市无关）。
package dashboard

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// TableWatch 单表监控项（白名单常量，无 SQL 注入面——表名/列名不入参数化）。
type TableWatch struct {
	Name      string        // 告警标识，如 "fin_trx_txs"
	WarnAfter time.Duration // 超过→warn
	CritAfter time.Duration // 超过→critical
}

// TableFreshnessStore 表级新鲜度监控所需的 store 接口（pgstore.Store 满足）。
type TableFreshnessStore interface {
	TableMaxTs(ctx context.Context, name string) (time.Time, error) // 返回该监控项最新时间戳
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// DefaultTableWatches v1 监控清单（warn≈2×正常周期，crit≈4×）。
// 阈值依据：正常周期来自生产实测，warn 留 2× 余量防误报，crit 留 4× 兜暗死。
var DefaultTableWatches = []TableWatch{
	{Name: "xasset_monitor", WarnAfter: 20 * time.Minute, CritAfter: 1 * time.Hour},
	{Name: "cex_spread", WarnAfter: 15 * time.Minute, CritAfter: 1 * time.Hour},
	{Name: "funding_hist_binance", WarnAfter: 12 * time.Hour, CritAfter: 26 * time.Hour},
	{Name: "trx_account", WarnAfter: 15 * time.Minute, CritAfter: 1 * time.Hour},
	{Name: "daily_income", WarnAfter: 26 * time.Hour, CritAfter: 30 * time.Hour},
}

// TableFreshnessMonitor 表级新鲜度监控器。
type TableFreshnessMonitor struct {
	store   TableFreshnessStore
	watches []TableWatch
	tick    time.Duration

	mu             sync.Mutex
	states         map[string]mtHealthMetric // name → 状态
	bootGraceTicks int                       // boot 宽限剩余 tick 数（>0 时只记状态不告警）
	lastCritAlert  map[string]time.Time      // name → 上次 crit 告警时间
	now            func() time.Time          // 可注入时钟（测试用），nil→time.Now
}

// NewTableFreshnessMonitor 创建表级新鲜度监控。
func NewTableFreshnessMonitor(store TableFreshnessStore, watches []TableWatch, tick time.Duration) *TableFreshnessMonitor {
	return &TableFreshnessMonitor{
		store:          store,
		watches:        watches,
		tick:           tick,
		states:         make(map[string]mtHealthMetric),
		bootGraceTicks: 3, // boot 宽限 3 tick 给采集器启动
		lastCritAlert:  make(map[string]time.Time),
	}
}

// Run 轮询直至 ctx 取消（模式同 MTHealthMonitor.Run）。
func (m *TableFreshnessMonitor) Run(ctx context.Context) {
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

// nowTime 返回当前时钟（可注入，nil→time.Now）。
func (m *TableFreshnessMonitor) nowTime() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// poll 一次采集 + 状态机转换 + 告警 + 日志。
func (m *TableFreshnessMonitor) poll(ctx context.Context) {
	now := m.nowTime()
	m.mu.Lock()
	inGrace := m.bootGraceTicks > 0
	if inGrace {
		m.bootGraceTicks--
	}
	prevStates := m.states
	newStates := make(map[string]mtHealthMetric, len(m.watches))
	m.mu.Unlock()

	var logParts []string
	for _, w := range m.watches {
		ts, err := m.store.TableMaxTs(ctx, w.Name)
		var age time.Duration
		raw := metricCrit // 查询失败/空表=异常
		if err != nil {
			log.Printf("[table-health] %s TableMaxTs: %v", w.Name, err)
		} else if ts.IsZero() {
			// 空表 → crit（从未采集=异常）
		} else if age = now.Sub(ts); age <= 0 {
			// D-055：未来/同刻 ts=串行查询间隙新行刚提交——按刚写鲜行判 healthy。
			// （classifyAge 的 age<=0→crit 是 mt_health「无数据」语义，此处 ts 有效不适用）
			raw = metricHealthy
		} else {
			raw = classifyAge(age, w.WarnAfter, w.CritAfter)
		}
		newStates[w.Name] = raw
		logParts = append(logParts, w.Name+"="+durStr(age))

		if inGrace {
			continue // boot 宽限内只记状态不告警
		}
		prev := prevStates[w.Name]
		m.maybeTableAlert(ctx, w.Name, prev, raw, age)
	}

	m.mu.Lock()
	m.states = newStates
	m.mu.Unlock()

	log.Printf("[table-health] %s", strings.Join(logParts, " "))
}

// maybeTableAlert 状态转换时插告警（同 mt-health maybeAlert 语义，无周末静默窗）。
// 持续同态不重复插（防洪水），crit >30min 重发一条（复用 critRealertInterval）。
func (m *TableFreshnessMonitor) maybeTableAlert(ctx context.Context, name string, prev, cur mtHealthMetric, age time.Duration) {
	if prev == cur {
		if cur != metricCrit {
			return
		}
		m.mu.Lock()
		last, ok := m.lastCritAlert[name]
		m.mu.Unlock()
		if ok && m.nowTime().Sub(last) < critRealertInterval {
			return
		}
	}
	var level, msg string
	subject := "表格新鲜度监控：" + name
	switch cur {
	case metricHealthy:
		level = "info"
		msg = subject + "已恢复（此前滞后 " + durStr(age) + "）"
	case metricWarn:
		level = "warn"
		msg = subject + "数据停更（" + durStr(age) + " 未写入）→ 检查对应采集任务"
	case metricCrit:
		level = "critical"
		msg = subject + "数据严重滞后（" + durStr(age) + " 未写入）→ 需立即人工介入"
	}
	if cur == metricCrit {
		m.mu.Lock()
		m.lastCritAlert[name] = m.nowTime()
		m.mu.Unlock()
	}
	if err := m.store.InsertAlert(ctx, nil, level, msg); err != nil {
		log.Printf("[table-health] alert insert: %v", err)
	}
	log.Printf("[table-health] %s: %s", level, msg)
}
