// CexExecutorManager CEX 下单执行器的单一真相源（D-059）。
// 每轮从 exchange_configs 读配置 → 对每条构建 exec.CEXExecutor 注册进 exec.Registry；
// diff 上一轮 key 集合，消失的注销。增删/改 key 在下一轮 tick 生效（与 AccountMonitor 同语义，不重启）。
// 镜像 D-054 AccountMonitor 的「DB 每轮重取」模式；reconcile 侧只读账户、本管理器可下单。
package dashboard

import (
	"context"
	"log"
	"sync"
	"time"

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
	// alertFn D-108 P2 T1：平仓被拒时 critical 告警注入（main 传 store.InsertAlert）。
	alertFn func(posID *int64, level, msg string)

	mu   sync.Mutex
	keys map[string]bool // 上轮已注册的 venueKey（diff 用）
}

// NewCexExecutorManager 创建管理器。interval 复用 cfg.BrokerPosPollInterval（D-059 不新增 knob）。
// alertFn D-108 P2 T1：平仓被拒 critical 告警（main 传 store.InsertAlert；nil=不告警）。
func NewCexExecutorManager(s ExchangeStore, reg *exec.Registry, interval time.Duration, alertFn func(posID *int64, level, msg string), marginReservePct float64) *CexExecutorManager {
	return &CexExecutorManager{
		store:            s,
		reg:              reg,
		interval:         interval,
		alertFn:          alertFn,
		marginReservePct: marginReservePct,
		keys:             make(map[string]bool),
	}
}

// Run 轮询直至 ctx 取消。启动即 sync 一次（首个注册立即可用）。
func (m *CexExecutorManager) Run(ctx context.Context) {
	m.sync(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sync(ctx)
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

	// D-362 S3：撞键归一（同 S1 函数同源调用）——binance:perp 赢家（sim）保 canonical：
	// allowlist 流量仍打 sim 执行器=零路由变更（D-158 安全语义保持，SSOT §1 案 C）；
	// 真盘 binance:perp#20 注册但无人调用=休眠可见。gate:perp 单 config 无撞键键不变。
	// D-362 S4① 核证：ARB_AUTOTRADE_ALLOW_VENUES 匹配在 engine_opp.go:61 用 opp 腿
	// venue（canonical 形），#id 后缀键天然不命中 allowlist 条目=安全。
	// D-362 S4② 核证：pipeline 执行路由 reg.Get(leg.VenueKey)（pipeline_execute.go:62 等）
	// 用仓腿 venue（canonical 形）→ 永不路由到 #id 后缀执行器=休眠不可达。
	// D-362 S4⑤ 核证：readiness executor_ready 消费 reg.Len()（exec.go:162）——计数语义
	// =已注册执行器数，休眠 real 执行器使计数 +1（上限语义非白名单，可接受）。
	winners := cexKeyWinners(cfgs)
	seen := make(map[string]bool, len(cfgs))
	for i := range cfgs {
		c := &cfgs[i]
		base := resolveExchangeBaseURL(*c)
		vk := reconcile.CexVenueKey(c.Venue, c.VenueType, c.ID, winners)
		seen[vk] = true
		exe := exec.NewCEXExecutor(c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase, base, c.Simulated)
		exe.SetKeyOverride(vk)
		exe.SetMarginReservePct(m.marginReservePct)
		exe.AlertFn = m.alertFn // D-108 P2 T1：平仓被拒 critical 告警注入
		m.reg.Register(exe)
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
}
