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
)

// CexExecutorManager 管理 CEX 执行器注册。
type CexExecutorManager struct {
	store    ExchangeStore
	reg      *exec.Registry
	interval time.Duration

	mu   sync.Mutex
	keys map[string]bool // 上轮已注册的 venueKey（diff 用）
}

// NewCexExecutorManager 创建管理器。interval 复用 cfg.BrokerPosPollInterval（D-059 不新增 knob）。
func NewCexExecutorManager(s ExchangeStore, reg *exec.Registry, interval time.Duration) *CexExecutorManager {
	return &CexExecutorManager{
		store:    s,
		reg:      reg,
		interval: interval,
		keys:     make(map[string]bool),
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

	seen := make(map[string]bool, len(cfgs))
	for i := range cfgs {
		c := &cfgs[i]
		base := resolveExchangeBaseURL(*c)
		vk := c.Venue + ":" + c.VenueType
		seen[vk] = true
		m.reg.Register(exec.NewCEXExecutor(c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase, base, c.Simulated))
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
