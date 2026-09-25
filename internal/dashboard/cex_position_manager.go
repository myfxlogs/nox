// cex_position_manager.go — CEX perp 持仓查询器管理器（D-121）。
// 每轮从 exchange_configs 读配置 → 对 perp 类型构建 CEXPositionQuerier → SetCEXQueriers 注入 BrokerManager。
// 镜像 CexExecutorManager 的「DB 每轮重取」模式；后台增删/改 key → 下轮 tick 生效，不重启。
package dashboard

import (
	"arb/internal/bus"
	"context"
	"log"
	"time"

	"arb/internal/collect/exchange"
	"arb/internal/reconcile"
)

// CexPositionManager 管理 CEX perp 持仓查询器注册到 BrokerManager（D-121）。
type CexPositionManager struct {
	bm       *BrokerManager
	store    ExchangeStore
	interval time.Duration
	// PositionCache D-450 S6：私有 WS 持仓事件缓存——非 nil 时各 querier 包装
	// CachedVenueQuerier（全量快照 fresh≤10s 直返，REST 真相源兜底不拆）。
	PositionCache *exchange.PositionEventCache

	keys map[string]bool // 上轮已注册的 venueKey（diff 日志用）
	// KickTopic D-469b S2：config 变更 kick 即到即跑（nil=纯 ticker）。
	KickTopic *bus.Topic[struct{}]
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

// sync 一次对齐：读 DB exchange_configs → 构建 perp querier 集合 → SetCEXQueriers。
func (m *CexPositionManager) sync(ctx context.Context) {
	cfgs, err := m.store.ListExchangeConfigs(ctx)
	if err != nil {
		log.Printf("[cex-pos] list exchanges: %v", err)
		return
	}
	// D-362 S2b：撞键归一（同 S1 函数同源调用）——binance:perp 双配置下赢家（sim，后建）
	// 保 canonical（demo 持仓视图不变），真盘 querier 键=binance:perp#20（真仓在持仓视图
	// 独立现形；BrokerPosition.VenueKey 随querier键带后缀=SSOT §2b）。
	winners := cexKeyWinners(cfgs)
	seen := make(map[string]bool, len(cfgs))
	var qs []reconcile.VenueQuerier
	for i := range cfgs {
		c := &cfgs[i]
		if c.VenueType != "perp" {
			continue // spot 无持仓查询
		}
		base := resolveExchangeBaseURL(*c)
		vk := reconcile.CexVenueKey(c.Venue, c.VenueType, c.ID, winners)
		seen[vk] = true
		q := reconcile.NewCEXPositionQuerier(c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase, base, c.Simulated)
		q.SetKeyOverride(vk)
		var vq reconcile.VenueQuerier = q
		if m.PositionCache != nil {
			// D-450：包装事件缓存加速层——REST 仍是真相源（miss/stale 回落）。
			vq = exchange.NewCachedVenueQuerier(q, m.PositionCache)
		}
		qs = append(qs, vq)
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
