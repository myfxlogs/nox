// cex_position_manager.go — CEX perp 持仓查询器管理器（D-121）。
// 每轮从 exchange_configs 读配置 → 对 perp 类型构建 CEXPositionQuerier → SetCEXQueriers 注入 BrokerManager。
// 镜像 CexExecutorManager 的「DB 每轮重取」模式；后台增删/改 key → 下轮 tick 生效，不重启。
package dashboard

import (
	"context"
	"log"
	"time"

	"arb/internal/reconcile"
)

// CexPositionManager 管理 CEX perp 持仓查询器注册到 BrokerManager（D-121）。
type CexPositionManager struct {
	bm       *BrokerManager
	store    ExchangeStore
	interval time.Duration

	keys map[string]bool // 上轮已注册的 venueKey（diff 日志用）
}

// NewCexPositionManager 创建管理器。interval 复用 cfg.BrokerPosPollInterval。
func NewCexPositionManager(bm *BrokerManager, s ExchangeStore, interval time.Duration) *CexPositionManager {
	return &CexPositionManager{
		bm:       bm,
		store:    s,
		interval: interval,
		keys:     make(map[string]bool),
	}
}

// Run 轮询直至 ctx 取消。启动即 sync 一次。
func (m *CexPositionManager) Run(ctx context.Context) {
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

// sync 一次对齐：读 DB exchange_configs → 构建 perp querier 集合 → SetCEXQueriers。
func (m *CexPositionManager) sync(ctx context.Context) {
	cfgs, err := m.store.ListExchangeConfigs(ctx)
	if err != nil {
		log.Printf("[cex-pos] list exchanges: %v", err)
		return
	}
	seen := make(map[string]bool, len(cfgs))
	var qs []reconcile.VenueQuerier
	for i := range cfgs {
		c := &cfgs[i]
		if c.VenueType != "perp" {
			continue // spot 无持仓查询
		}
		base := resolveExchangeBaseURL(*c)
		vk := c.Venue + ":" + c.VenueType
		seen[vk] = true
		qs = append(qs, reconcile.NewCEXPositionQuerier(c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase, base, c.Simulated))
		if !m.keys[vk] {
			log.Printf("[cex-pos] register %s (base=%s simulated=%v)", vk, base, c.Simulated)
		}
	}
	for vk := range m.keys {
		if !seen[vk] {
			log.Printf("[cex-pos] unregister %s", vk)
		}
	}
	m.keys = seen
	m.bm.SetCEXQueriers(qs)
}
