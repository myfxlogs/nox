// cex_position_manager.go — CEX perp 持仓查询器管理器（D-121）。
// 每轮从 exchange_configs 读配置 → 对 perp 类型构建 CEXPositionQuerier → SetCEXQueriers 注入 BrokerManager。
// 镜像 CexExecutorManager 的「DB 每轮重取」模式；后台增删/改 key → 下轮 tick 生效，不重启。
package dashboard

import (
	"arb/internal/bus"
	"arb/internal/venue"
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

	keys      map[string]bool // 上轮已注册的 venueKey（diff 日志用）
	simWarned map[int64]bool  // D-516：simulated=t 行 fail-closed warn-once
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
	// D-362 S2b：撞键归一（同 S1 函数同源调用）——同 venue:type 多 config 赢家保
	// canonical、输家键带 #id 后缀（BrokerPosition.VenueKey 随 querier 键=SSOT §2b）。
	// D-516：sim 行先于归一剔除（不占 canonical、不建查询器）。sync 仅 Run 单协程驱动，
	// simWarned 无并发面。
	live := realOnlyConfigs(cfgs, &m.simWarned, "cex-pos")
	winners := cexKeyWinners(live)
	seen := make(map[string]bool, len(live))
	var qs []venue.Querier
	var newKeys []string // D-009 S1：本轮新注册键（boot 竞态叫醒用）
	for i := range live {
		c := &live[i]
		if c.VenueType != "perp" {
			continue // spot 无持仓查询
		}
		base := resolveExchangeBaseURL(*c)
		vk := reconcile.CexVenueKey(c.Venue, c.VenueType, c.ID, winners)
		seen[vk] = true
		q := reconcile.NewCEXPositionQuerier(c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase, base, c.Simulated)
		q.SetKeyOverride(vk)
		var vq venue.Querier = q
		if m.PositionCache != nil {
			// D-450：包装事件缓存加速层——REST 仍是真相源（miss/stale 回落）。
			vq = exchange.NewCachedVenueQuerier(q, m.PositionCache)
		}
		qs = append(qs, vq)
		if !m.keys[vk] {
			log.Printf("[cex-pos] register %s (base=%s simulated=%v)", vk, base, c.Simulated)
			newKeys = append(newKeys, vk)
		}
	}
	for vk := range m.keys {
		if !seen[vk] {
			log.Printf("[cex-pos] unregister %s", vk)
		}
	}
	m.keys = seen
	m.bm.SetCEXQueriers(qs)
	// D-009 S1：新注册键叫醒 bpMon 回读——boot 竞态实证（bpm.Run 首轮回读早于
	// CEX querier 注册则该 venue 永不补读，restOnline 未知/持仓缺席直到下一 kick）。
	// pendingKick 幂等去重，Run 未起时信号驻留 cap1 亦不丢。
	for _, vk := range newKeys {
		m.bm.KickPositions(vk)
	}
}
