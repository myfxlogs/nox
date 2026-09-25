// cex_stream_manager.go —— D-450 S6：CEX 私有 WS 流生命周期管理器。
// 镜像 CexExecutorManager「DB 每轮重取」模式：exchange_configs 每轮读 →
// gate:perp 行起 GatePrivateWS、binance 行起 BinanceUserWS（perp real/demo 各一条）；
// 行消失→停流（cancel）；行不变→不重启。REST 轮询双轨保留不拆。
// binance:spot 行走 ws-api 支路（D-459：REST userDataStream 410 下线后实证
// userDataStream.start/ping 免签名管理面可用；session.logon 拒 HMAC 不走）。
package dashboard

import (
	"context"
	"log"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/httpx"

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
// CexStreamManager CEX 私有 WS 流管理器（D-450；D-469a 扩：余额 sink kick acctMon+
// binance a.m hooks 注入——字段注入先例同 cexExecMgr.OrderCache，装配处零值=不 kick）。
type CexStreamManager struct {
	// AcctKick D-469a S3：余额快照落库后回调（目标=acctMon.Kick()，nil 安全）。
	AcctKick func()
	// BinanceHooks D-469a S3：a.m 事件原因回调（OnFundingFee→income/OnIO→cex-io，nil 安全）。
	BinanceHooks exchange.BinanceUserHooks
	// OKXHooks D-469b S1：OKX 四频道事件回调（funding_fee→okx income/account→acctMon/
	// orders→bpMon，nil 安全）。
	OKXHooks exchange.OKXPrivateHooks
	// KickTopic D-469b S2：config 变更 kick（exchange RPC mutation → publish，sync 即到即跑；
	// nil=纯 ticker）。chan 扇出用 bus.Topic——多 manager/热载器共享同一事件源。
	KickTopic *bus.Topic[struct{}]

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
	var kickCh <-chan struct{}
	var cancelSub func()
	if m.KickTopic != nil {
		kickCh, cancelSub = m.KickTopic.Subscribe()
		defer cancelSub()
	}
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-ticker.C:
			m.sync(ctx)
		case <-kickCh:
			m.sync(ctx) // D-469b S2：config mutation kick 即到即跑
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
		// D-469a S3：余额快照 sink 包 kicking wrapper（落库成功→acctMon kick；写失败不 kick）。
		snapSink := exchange.NewKickingSnapshotSink(m.sinks, m.AcctKick)
		switch c.Venue {
		case "gate":
			ws := exchange.NewGatePrivateWS(c.APIKey, c.APISecret, c.BaseURL, vk, c.Simulated, snapSink, m.sinks, m.orderCache, m.posCache)
			safego.Go("cex-stream:gate", func() { ws.Run(sctx) })
		case "binance":
			ws := exchange.NewBinanceUserWS(c.APIKey, c.APISecret, c.BaseURL, c.VenueType, vk, c.Simulated, snapSink, m.sinks, m.orderCache, m.posCache, m.BinanceHooks)
			safego.Go("cex-stream:binance", func() { ws.Run(sctx) })
		case "okx":
			// D-469b S1：OKX 私有流纳入 manager 生命周期（boot 一次性装配已移除）。
			ws := exchange.NewOKXPrivateWS(c.APIKey, c.APISecret, c.Passphrase, c.Simulated, snapSink, vk, m.sinks, m.orderCache, m.posCache, m.OKXHooks)
			// D-477 S3（GAP-B4）：ctValFor 注入——resolveExchangeBaseURL(c) 与 executor
			// baseURL 同源（venue config/proxy 链），撤 www.okx.com 硬编码双源分叉。
			okxBase := resolveExchangeBaseURL(c)
			ws.SetCtValFor(func(ctx context.Context, instID string) (float64, error) {
				ctVal, _, err := exec.OkxSwapSpec(ctx, httpx.Client(), okxBase, instID)
				return ctVal, err
			})
			safego.Go("cex-stream:okx", func() { ws.Run(sctx) })
		}
		m.streams[id] = cancel
		log.Printf("[cex-stream] start cfg#%d %s:%s vk=%s simulated=%v", id, c.Venue, c.VenueType, vk, c.Simulated)
	}
}

// streamable 哪些配置行起流：gate:perp + binance:perp（real/demo）+ binance:spot（仅 real——
// spot testnet ws-api 端点不在代理路由内，demo 行保持 REST 轮询兜底，D-459 实证结论）
// + okx:perp（D-469b S1：凭据三件套齐 real/demo 同规则）。
func streamable(c pgstore.ExchangeConfig) bool {
	switch c.Venue {
	case "gate":
		return c.VenueType == "perp" && c.BaseURL != "" // base 空=warn 跳过不直连（白名单语义）
	case "binance":
		if c.VenueType == "spot" {
			return c.BaseURL != "" && !c.Simulated
		}
		return c.VenueType == "perp" && c.BaseURL != ""
	case "okx":
		// D-469b S1：okx:perp 凭据三件套齐即可起（simulated 行同规则可起——与 gate/binance 同）。
		return c.VenueType == "perp" && c.APIKey != "" && c.APISecret != "" && c.Passphrase != ""
	}
	return false
}
