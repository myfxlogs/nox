// cex_stream_manager.go —— D-450 S6：CEX 私有 WS 流生命周期管理器。
// 镜像 CexExecutorManager「DB 每轮重取」模式：exchange_configs 每轮读 →
// gate:perp 行起 GatePrivateWS、binance 行起 BinanceUserWS（perp real/demo 各一条）；
// 行消失→停流（cancel）；行不变→不重启。REST 轮询双轨保留不拆。
// binance:spot 行跳过——REST userDataStream 端点 410 Gone 已下线（生产实证，
// ws-api 替代协议属新设计域待决策），spot 数据面继续走 REST 轮询兜底。
package dashboard

import (
	"context"
	"log"
	"sync"
	"time"

	"arb/internal/collect/exchange"
	"arb/internal/reconcile"
	"arb/internal/safego"
	"arb/internal/store/pgstore"
)

// CexStreamSinks 流写口集合（*pgstore.Store 全实现）。
type CexStreamSinks interface {
	exchange.VenueSnapshotSink
	exchange.PrivateEventSink
}

// CexStreamManager 管理 gate/binance 私有 WS 流。
type CexStreamManager struct {
	store      ExchangeStore
	sinks      CexStreamSinks
	interval   time.Duration
	orderCache *exchange.OrderEventCache
	posCache   *exchange.PositionEventCache

	mu      sync.Mutex
	streams map[int64]context.CancelFunc // config row id → cancel
}

// NewCexStreamManager 创建管理器。缓存由 main 构造并与 Cached*Querier 共享。
func NewCexStreamManager(s ExchangeStore, sinks CexStreamSinks, interval time.Duration, oc *exchange.OrderEventCache, pc *exchange.PositionEventCache) *CexStreamManager {
	return &CexStreamManager{
		store: s, sinks: sinks, interval: interval,
		orderCache: oc, posCache: pc,
		streams: map[int64]context.CancelFunc{},
	}
}

// Run 轮询直至 ctx 取消（启动即 sync 一次）。
func (m *CexStreamManager) Run(ctx context.Context) {
	m.sync(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-ticker.C:
			m.sync(ctx)
		}
	}
}

func (m *CexStreamManager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, cancel := range m.streams {
		cancel()
		delete(m.streams, id)
	}
}

// sync 一次对齐：配置行→流。撞键归一键与 executor/position querier 同算法（同源 CexVenueKey）。
func (m *CexStreamManager) sync(ctx context.Context) {
	cfgs, err := m.store.ListExchangeConfigs(ctx)
	if err != nil {
		log.Printf("[cex-stream] list exchanges: %v", err)
		return
	}
	winners := cexKeyWinners(cfgs)

	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[int64]pgstore.ExchangeConfig{}
	for i := range cfgs {
		c := cfgs[i]
		if !streamable(c) {
			continue
		}
		want[c.ID] = c
	}
	// 停已消失的行。
	for id, cancel := range m.streams {
		if _, ok := want[id]; !ok {
			cancel()
			delete(m.streams, id)
			log.Printf("[cex-stream] stop cfg#%d", id)
		}
	}
	// 起新行（行不变→已在 streams 中，跳过）。
	for id, c := range want {
		if _, ok := m.streams[id]; ok {
			continue
		}
		vk := reconcile.CexVenueKey(c.Venue, c.VenueType, c.ID, winners)
		sctx, cancel := context.WithCancel(ctx)
		switch c.Venue {
		case "gate":
			ws := exchange.NewGatePrivateWS(c.APIKey, c.APISecret, c.BaseURL, vk, c.Simulated, m.sinks, m.sinks, m.orderCache, m.posCache)
			safego.Go("cex-stream:gate", func() { ws.Run(sctx) })
		case "binance":
			ws := exchange.NewBinanceUserWS(c.APIKey, c.APISecret, c.BaseURL, c.VenueType, vk, c.Simulated, m.sinks, m.sinks, m.orderCache, m.posCache)
			safego.Go("cex-stream:binance", func() { ws.Run(sctx) })
		}
		m.streams[id] = cancel
		log.Printf("[cex-stream] start cfg#%d %s:%s vk=%s simulated=%v", id, c.Venue, c.VenueType, vk, c.Simulated)
	}
}

// streamable 哪些配置行起流：gate:perp + binance:perp（real/demo）。
// binance:spot 跳过（REST userDataStream 端点 410 已下线，见文件头注）；gate 仅 perp（SSOT 边界）。
func streamable(c pgstore.ExchangeConfig) bool {
	switch c.Venue {
	case "gate":
		return c.VenueType == "perp" && c.BaseURL != "" // base 空=warn 跳过不直连（白名单语义）
	case "binance":
		return c.VenueType == "perp" && c.BaseURL != ""
	}
	return false
}
