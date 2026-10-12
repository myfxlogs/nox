// CexExecutorManager CEX 下单执行器的单一真相源（D-059）。
// 每轮从 exchange_configs 读配置 → 对每条构建 exec.CEXExecutor 注册进 exec.Registry；
// diff 上一轮 key 集合，消失的注销。增删/改 key 在下一轮 tick 生效（与 AccountMonitor 同语义，不重启）。
// 镜像 D-054 AccountMonitor 的「DB 每轮重取」模式；reconcile 侧只读账户、本管理器可下单。
package dashboard

import (
	"arb/internal/bus"
	"context"
	"log"
	"sync"
	"time"

	"arb/internal/collect/exchange"
	"arb/internal/exec"
	"arb/internal/reconcile"
)

// CexExecutorManager 管理 CEX 执行器注册。
type CexExecutorManager struct {
	store    ExchangeStore
	reg      *exec.Registry
	interval time.Duration
	// marginReservePct D-415：CEX executor 资金预检安全垫注入值（cfg.MarginReservePct）。
	marginReservePct float64
	// D-472 S2/S4：强平距离闸与 gate 缺省杠杆注入值（cfg 同源，per-executor Set 注入）。
	minLiqDistPct          float64
	crossNotionalEquityCap float64
	// D-473 S2：spot 现金预检 taker 缓冲注入值（SetSpotTakerBps；零值=不缓冲）。
	spotTaker     map[string]float64
	spotTakerDflt float64
	// alertFn D-108 P2 T1：平仓被拒时 critical 告警注入（main 传 store.InsertAlert）。
	alertFn   func(posID *int64, level, msg string)
	simWarned map[int64]bool // D-516：simulated=t 行 fail-closed warn-once
	// OrderStatusHook D-421 §1.5：exchange_configs 刷新后镜像订单级查单器集合
	// → recon.SetOrderStatusQueriers（nil=未接线）。
	OrderStatusHook func(map[string]reconcile.OrderStatusQuerier)
	// OrderCache D-450 S6：私有 WS 订单事件缓存——非 nil 时 osqs 各键包装
	// CachedOrderStatusQuerier（新鲜事件直返，REST 真相源兜底不拆）。
	OrderCache *exchange.OrderEventCache

	mu   sync.Mutex
	keys map[string]bool // 上轮已注册的 venueKey（diff 用）
	// KickTopic D-469b S2：config 变更 kick 即到即跑（nil=纯 ticker）。
	KickTopic *bus.Topic[struct{}]
}

// NewCexExecutorManager 创建管理器。interval 复用 cfg.BrokerPosPollInterval（D-059 不新增 knob）。
// alertFn D-108 P2 T1：平仓被拒 critical 告警（main 传 store.InsertAlert；nil=不告警）。
// minLiqDistPct/crossNotionalEquityCap 注入构造（D-472 S2/S4，零值=缺省语义）。
func NewCexExecutorManager(s ExchangeStore, reg *exec.Registry, interval time.Duration, alertFn func(posID *int64, level, msg string), marginReservePct float64) *CexExecutorManager {
	return &CexExecutorManager{
		store:            s,
		reg:              reg,
		interval:         interval,
		alertFn:          alertFn,
		marginReservePct: marginReservePct,
		// D-472 S2/S4：构造缺省（cfg setter 未调用时仍保有语义正确的档位——
		// cross cap 零值注入会静默关闸，故缺省=包级常量非零值）。
		crossNotionalEquityCap: exec.DefaultCrossNotionalEquityCap,
		keys:                   make(map[string]bool),
	}
}

// SetLiqGuards D-472 S4：cfg 注入强平闸参数（ARB_MIN_LIQ_DISTANCE_PCT/ARB_CROSS_NOTIONAL_EQUITY_CAP）。
func (m *CexExecutorManager) SetLiqGuards(minLiqDistPct, crossNotionalEquityCap float64) {
	m.minLiqDistPct = minLiqDistPct
	m.crossNotionalEquityCap = crossNotionalEquityCap
}

// SetSpotTakerBps D-473 S2：cfg 注入 spot 腿 taker 费率表（cfg.CommissionTaker 逐所 +
// CommissionCEXTakerBps 回退），现金预检缓冲用——与 evaluator commissionFor 同一取值源。
func (m *CexExecutorManager) SetSpotTakerBps(perVenue map[string]float64, fallback float64) {
	m.spotTaker = perVenue
	m.spotTakerDflt = fallback
}

// spotTakerFor sync 内按 venue 取费率（map 命中优先，否则回退缺省）。
func (m *CexExecutorManager) spotTakerFor(venue string) float64 {
	if m.spotTaker != nil {
		if bps, ok := m.spotTaker[venue]; ok {
			return bps
		}
	}
	return m.spotTakerDflt
}

// Run 轮询直至 ctx 取消。启动即 sync 一次（首个注册立即可用）。
func (m *CexExecutorManager) Run(ctx context.Context) {
	m.sync(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	var kickCh <-chan struct{}
	var cancelSub func()
	if m.KickTopic != nil {
		kickCh, cancelSub = m.KickTopic.Subscribe()
		defer cancelSub()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sync(ctx)
		case <-kickCh:
			m.sync(ctx) // D-469b S2：config mutation kick 即到即跑
		}
	}
}

// sync 一次对齐：注册全部配置 executor，注销已消失的。
func (m *CexExecutorManager) sync(ctx context.Context) {
	cfgs, err := m.store.ListExchangeConfigs(ctx)
	if err != nil {
		log.Printf("[cex-exec] list exchanges: %v", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// D-362 S3：撞键归一（同 S1 函数同源调用）——同 venue:type 多 config 赢家保 canonical、
	// 输家键带 #id 后缀：allowlist/pipeline 路由只命中 canonical 键（D-158 安全语义保持，
	// SSOT §1 案 C）；输家执行器注册但无人调用=休眠可见。gate:perp 单 config 无撞键键不变。
	// D-516：sim 行不入集（realOnlyConfigs）——canonical 恒归 real 赢家。
	// D-362 S4① 核证：ARB_AUTOTRADE_ALLOW_VENUES 匹配在 engine_opp.go:61 用 opp 腿
	// venue（canonical 形），#id 后缀键天然不命中 allowlist 条目=安全。
	// D-362 S4② 核证：pipeline 执行路由 reg.Get(leg.VenueKey)（pipeline_execute.go:62 等）
	// 用仓腿 venue（canonical 形）→ 永不路由到 #id 后缀执行器=休眠不可达。
	// D-362 S4⑤ 核证：readiness executor_ready 消费 reg.Len()（exec.go:162）——计数语义
	// =已注册执行器数，休眠 real 执行器使计数 +1（上限语义非白名单，可接受）。
	// D-516：sim 行先于撞键归一剔除——不占 canonical 键也不注册执行器（绝不拨 demo 端点）。
	live := realOnlyConfigs(cfgs, &m.simWarned, "cex-exec")
	winners := cexKeyWinners(live)
	seen := make(map[string]bool, len(live))
	osqs := make(map[string]reconcile.OrderStatusQuerier, len(live)) // D-421 §1.5
	for i := range live {
		c := &live[i]
		base := resolveExchangeBaseURL(*c)
		vk := reconcile.CexVenueKey(c.Venue, c.VenueType, c.ID, winners)
		seen[vk] = true
		exe := exec.NewCEXExecutor(c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase, base, c.Simulated)
		exe.SetKeyOverride(vk)
		exe.SetMarginReservePct(m.marginReservePct)
		exe.SetLiqGuards(m.minLiqDistPct, m.crossNotionalEquityCap) // D-472 S4
		exe.SetSpotTakerBps(m.spotTakerFor(c.Venue))                // D-473 S2
		exe.AlertFn = m.alertFn                                     // D-108 P2 T1：平仓被拒 critical 告警注入
		m.reg.Register(exe)
		osqs[vk] = exe // D-421：订单级查单面同生命周期镜像
		if m.OrderCache != nil {
			// D-018 R1：maker 等待唤醒源——WS 订单帧写缓存即扇出（AwaitPostOnlyFill
			// 订阅回读快照零 REST；订阅随等待生命周期，executor 只持闭包）。
			cache := m.OrderCache
			exe.SetOrderWake(cache.SubscribeEvents)
			// D-450：包装事件缓存加速层——REST 仍是真相源（miss/stale 回落）。
			osqs[vk] = exchange.NewCachedOrderStatusQuerier(vk, exe, m.OrderCache)
			// D-569 包B S4：binance 确认桥接——PeekOrder 闭包注入执行器。
			// D-599 S1：事件帧 n（USD 系口径）已入 OrderEvent.FeeUSD → 透传进 snap。
			if c.Venue == "binance" {
				cache := m.OrderCache
				exe.SetBinanceOrderLookup(func(ordID string) (exec.BinanceOrderSnap, bool) {
					ev, ok := cache.PeekOrder(vk, ordID)
					return exec.BinanceOrderSnap{State: ev.State, AccFillQty: ev.AccFillQty, AvgPx: ev.AvgPx, FeeUSD: ev.FeeUSD}, ok
				})
			}
			// D-566 包B S4-d：okx 确认桥接——PeekOrder 闭包（事件 FeeUSD 原生，费零 REST）。
			if c.Venue == "okx" {
				cache := m.OrderCache
				exe.SetOKXOrderLookup(func(ordID string) (exec.OKXOrderSnap, bool) {
					ev, ok := cache.PeekOrder(vk, ordID)
					return exec.OKXOrderSnap{State: ev.State, AccFillQty: ev.AccFillQty, AvgPx: ev.AvgPx, FeeUSD: ev.FeeUSD}, ok
				})
			}
			// D-570 包B S4③：gate 确认桥接——PeekOrder 闭包（帧原生 fee→FeeUSD 回填）。
			if c.Venue == "gate" {
				cache := m.OrderCache
				exe.SetGateOrderLookup(func(ordID string) (exec.GateOrderSnap, bool) {
					ev, ok := cache.PeekOrder(vk, ordID)
					return exec.GateOrderSnap{State: ev.State, AccFillQty: ev.AccFillQty, AvgPx: ev.AvgPx, FeeUSD: ev.FeeUSD}, ok
				})
			}
		}
		if !m.keys[vk] {
			log.Printf("[cex-exec] register %s (base=%s simulated=%v)", vk, base, c.Simulated)
		}
	}

	// 注销已消失的配置（上轮有、这轮无）。
	for vk := range m.keys {
		if !seen[vk] {
			m.reg.Unregister(vk)
			log.Printf("[cex-exec] unregister %s", vk)
		}
	}
	m.keys = seen
	if m.OrderStatusHook != nil {
		m.OrderStatusHook(osqs)
	}
}
