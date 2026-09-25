package position

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/safego"
	"arb/internal/store/pgstore"
)

// PersonalMonitor 监控个人持仓价格阈值（财务线 TRX 持仓接入，D-046）。
// 与 Monitor 的区别：Monitor 按 expected_net_bps 判建议平仓；personal 按价格阈值报警。
// 两种无关语义不并入同一结构体（P3），同包并列独立类型（设计文档 §4.1）。
type PersonalMonitor struct {
	kickTopic *bus.Topic[struct{}] // D-469b S3
	bus       *bus.QuoteBus
	store     *pgstore.Store
	mu        sync.Mutex
	positions map[int64]*pgstore.PersonalPosition // 缓存 OPEN 行
	level     map[int64]string                    // id → 'ok'|'warn'|'crit'（边缘触发状态）
	lastAlert map[int64]time.Time                 // id → 上次告警时间（5s 节流）
	throttle  time.Duration
	// alertFn 告警写入通道（默认 store.InsertAlert rule_id=NULL 系统级）；测试注入。
	alertFn func(ctx context.Context, level, message string) error
	// now 可注入时钟（测试节流 + 报价陈旧判定）。
	now func() time.Time

	// D-049 实时现价广播：与告警正交（不走 5s 节流）。
	freshTTL  time.Duration       // 报价陈旧 TTL（复用 config.DetectorFreshnessTTL，G3）
	liqMMR    float64             // 理论强平维持保证金率（config.ARB_LIQ_MMR）
	onPrice   func(PositionPrice) // 现价视图回调（nil 安全；main 接 priceTopic.Publish）
	lastPrice map[int64]float64   // id → 上次广播价（同价去重，防同值连发）

	// D-208 v2：CMC 数据源（nil = 不支持 CMC 行，走老路径）。
	cmcSource CMCSource
}

// NewPersonalMonitor 创建个人持仓监控器。onPrice 允许 nil（不广播，仅告警）。
func NewPersonalMonitor(b *bus.QuoteBus, store *pgstore.Store, freshTTL time.Duration, liqMMR float64, onPrice func(PositionPrice)) *PersonalMonitor {
	m := &PersonalMonitor{
		bus:       b,
		store:     store,
		freshTTL:  freshTTL,
		liqMMR:    liqMMR,
		positions: make(map[int64]*pgstore.PersonalPosition),
		level:     make(map[int64]string),
		lastAlert: make(map[int64]time.Time),
		throttle:  5 * time.Second,
		alertFn: func(ctx context.Context, level, message string) error {
			return store.InsertAlert(ctx, nil, level, message)
		},
		now: time.Now,
	}
	if onPrice != nil {
		m.onPrice = onPrice
		m.lastPrice = make(map[int64]float64)
	}
	return m
}

// SetCMCSource 注入 CMC 数据源（D-208 v2：nil 安全=走老路径）。
func (m *PersonalMonitor) SetCMCSource(src CMCSource) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cmcSource = src
}

// Run 启动监控。阻塞至 ctx 取消。
func (m *PersonalMonitor) Run(ctx context.Context) {
	if err := m.loadPositions(ctx); err != nil {
		log.Printf("[personal-monitor] load: %v", err)
	}
	safego.Go("personal_monitor:refreshLoop", func() { m.refreshLoop(ctx) })

	ch, cancel := m.bus.Subscribe("")
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case q, ok := <-ch:
			if !ok {
				return
			}
			m.onQuote(ctx, q)
		}
	}
}

// loadPositions 加载 OPEN 个人持仓；CLOSED 行从缓存剔除并清理状态。
func (m *PersonalMonitor) loadPositions(ctx context.Context) error {
	positions, err := m.store.ListOpenPersonalPositions(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := make(map[int64]bool, len(positions))
	for i := range positions {
		p := &positions[i]
		seen[p.ID] = true
		m.positions[p.ID] = p
	}
	for id := range m.positions {
		if !seen[id] {
			delete(m.positions, id)
			delete(m.level, id)
			delete(m.lastAlert, id)
			delete(m.lastPrice, id) // D-049：广播价缓存随仓清理（closed 行不留残；nil map delete 安全）
		}
	}
	log.Printf("[personal-monitor] loaded %d open personal positions", len(m.positions))
	return nil
}

// SetKickTopic D-469b S3：positions 表变更事件订阅源（nil=纯 5min ticker 兜底）。
func (m *PersonalMonitor) SetKickTopic(t *bus.Topic[struct{}]) { m.kickTopic = t }

func (m *PersonalMonitor) refreshLoop(ctx context.Context) {
	// D-469b S3：持仓变更事件主路+5min 自愈兜底。
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	var kickCh <-chan struct{}
	var cancelSub func()
	if m.kickTopic != nil {
		kickCh, cancelSub = m.kickTopic.Subscribe()
		defer cancelSub()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-kickCh:
			if err := m.loadPositions(ctx); err != nil {
				log.Printf("[personal-monitor] refresh: %v", err)
			}
		case <-ticker.C:
			if err := m.loadPositions(ctx); err != nil {
				log.Printf("[personal-monitor] refresh: %v", err)
			}
		}
	}
}

// onQuote 按 price_symbol 过滤报价，估值后驱动边缘触发状态机。
func (m *PersonalMonitor) onQuote(ctx context.Context, q bus.Quote) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.positions {
		if p.PriceSymbol != q.Symbol {
			continue
		}
		mid := q.Mid()
		newLevel := PersonalLevel(mid, p.WarnHigh, p.CritHigh)
		prev := m.level[p.ID]
		if prev == "" {
			prev = "ok" // 首次报价以 ok 为基线，升至 warn/crit 触发告警
		}
		// 仅 level 上升（严格更危险）时告警；5s 节流兜底边界抖动
		if severityRank(newLevel) > severityRank(prev) && m.now().Sub(m.lastAlert[p.ID]) >= m.throttle {
			m.alert(ctx, p, newLevel, mid)
			m.lastAlert[p.ID] = m.now()
		}
		m.level[p.ID] = newLevel
	}

	// 实时现价广播（D-049）：与告警正交、不走 5s 节流。用 bus.Snapshot + LatestQuote 同选帧
	// （多 venue 同符号与快照路径取同一最新未陈旧帧，实时价 = 快照价，30s 对账边界不跳变）。
	// D-208 v2：CMC 行（cmc_instrument_id 非空 + cmcSource 注入）走 ComputeCMCPositionPrice。
	if m.onPrice != nil && m.bus != nil {
		syms := make([]string, 0, 1)
		for _, p := range m.positions {
			if p.PriceSymbol == q.Symbol {
				syms = append(syms, p.PriceSymbol)
				break
			}
		}
		if len(syms) > 0 {
			venues := m.bus.Snapshot(ctx, syms)
			for _, p := range m.positions {
				if p.PriceSymbol != q.Symbol {
					continue
				}
				var view PositionPrice
				if p.CMCInstrumentID != nil && *p.CMCInstrumentID != "" && m.cmcSource != nil {
					view = ComputeCMCPositionPrice(p, m.cmcSource, m.freshTTL, m.now())
				} else {
					view = ComputePositionPrice(p, venues[p.PriceSymbol], m.freshTTL, m.now(), m.liqMMR)
				}
				if view.Price != m.lastPrice[p.ID] {
					m.lastPrice[p.ID] = view.Price
					m.onPrice(view)
				}
			}
		}
	}
}

func (m *PersonalMonitor) alert(ctx context.Context, p *pgstore.PersonalPosition, level string, mid float64) {
	alertLevel := level
	if alertLevel == "crit" {
		alertLevel = "critical" // alerts 表 level CHECK ('info','warn','critical')
	}
	var msg string
	switch level {
	case "warn":
		msg = p.Asset + "(" + p.Kind + ") 价格 " + fmtFloat(mid) + " ≥ " + fmtFloat(*p.WarnHigh) + "，主动平仓线"
	case "crit":
		msg = p.Asset + "(" + p.Kind + ") 价格 " + fmtFloat(mid) + " ≥ " + fmtFloat(*p.CritHigh) + "，强平底线"
	default:
		return
	}
	if err := m.alertFn(ctx, alertLevel, msg); err != nil {
		log.Printf("[personal-monitor] alert %d: %v", p.ID, err)
	}
}

// PersonalLevel 按价格阈值映射告警级别（ok→warn→crit 严格上升）。
// warnHigh/critHigh 为 nil = 该级别不设；crit 优先于 warn。
// crit < warn 的防御：crit 命中即取 crit（更危险方向，安全侧）。
func PersonalLevel(mid float64, warnHigh, critHigh *float64) string {
	if critHigh != nil && mid >= *critHigh {
		return "crit"
	}
	if warnHigh != nil && mid >= *warnHigh {
		return "warn"
	}
	return "ok"
}

func severityRank(level string) int {
	switch level {
	case "crit":
		return 2
	case "warn":
		return 1
	default:
		return 0
	}
}

// fmtFloat 最短十进制表示（0.36 → "0.36"，0.3695 → "0.3695"）。
func fmtFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
