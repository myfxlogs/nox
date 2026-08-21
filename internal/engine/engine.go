// Package engine 串联采集→检测→评估→存储的事件驱动管线（§5/§6/D-026）。
package engine

import (
	"context"
	"log"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/detector"
	"arb/internal/evaluator"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// opportunityStore 引擎所需的机会存储接口（D-050 测试注入：生产为 pgstore.Store，测试用 fake 判定 inserted）。
type opportunityStore interface {
	UpsertOpportunity(ctx context.Context, o *pgstore.Opportunity) (bool, error)
}

// FactCache 实现 detector.FactProvider，从内存缓存提供 funding/swap/trade_mode Fact。
type FactCache struct {
	mu      sync.RWMutex
	funding map[string]map[string]float64    // canonical → venue → 年化
	swap    map[string]map[string][2]float64 // canonical → venue → [long, short]
	// tradeMode 键口径与 swap 不同（D-060 F4）：canonical → venueKey（"{venue}:{src}"，
	// Fact.Venue+":"+Fact.Src 拼，MT 侧 src=mt5/mt4）——消费方是 VenueKey() 语法
	//（检测器腿/执行腿），swap 消费方是 Quote.Venue 名键，两者各按各自消费方，不得互相对齐。
	tradeMode    map[string]map[string]tradeModeEntry
	tradeModeTTL time.Duration
}

// tradeModeEntry trade_mode 值 + 到达时间（TTL 新鲜度判据）。
type tradeModeEntry struct {
	mode int64
	ts   time.Time
}

func NewFactCache() *FactCache {
	return &FactCache{
		funding:      make(map[string]map[string]float64),
		swap:         make(map[string]map[string][2]float64),
		tradeMode:    make(map[string]map[string]tradeModeEntry),
		tradeModeTTL: 24 * time.Hour, // 缺省兜底；main 注入 config.TradeModeTTL（D-060）
	}
}

// SetTradeModeTTL 注入 trade_mode 新鲜度（config.ARB_TRADEMODE_TTL）。boot 期一次性调用。
func (fc *FactCache) SetTradeModeTTL(d time.Duration) {
	if d <= 0 {
		return
	}
	fc.mu.Lock()
	fc.tradeModeTTL = d
	fc.mu.Unlock()
}

func (fc *FactCache) UpdateFacts(facts []fact.Fact) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	for _, f := range facts {
		switch f.Kind {
		case fact.KindFunding:
			if fc.funding[f.Symbol] == nil {
				fc.funding[f.Symbol] = make(map[string]float64)
			}
			fc.funding[f.Symbol][f.Venue] = f.Value
		case fact.KindSwapLong, fact.KindSwapShort:
			if fc.swap[f.Symbol] == nil {
				fc.swap[f.Symbol] = make(map[string][2]float64)
			}
			entry := fc.swap[f.Symbol][f.Venue]
			if f.Kind == fact.KindSwapLong {
				entry[0] = f.Value
			} else {
				entry[1] = f.Value
			}
			fc.swap[f.Symbol][f.Venue] = entry
		case fact.KindTradeMode:
			if fc.tradeMode[f.Symbol] == nil {
				fc.tradeMode[f.Symbol] = make(map[string]tradeModeEntry)
			}
			fc.tradeMode[f.Symbol][f.Venue+":"+f.Src] = tradeModeEntry{mode: int64(f.Value), ts: f.Ts}
		}
	}
}

func (fc *FactCache) FundingFacts() map[string]map[string]float64 {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	cp := make(map[string]map[string]float64, len(fc.funding))
	for k, v := range fc.funding {
		cp[k] = make(map[string]float64, len(v))
		for kk, vv := range v {
			cp[k][kk] = vv
		}
	}
	return cp
}

func (fc *FactCache) SwapFacts() map[string]map[string][2]float64 {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	cp := make(map[string]map[string][2]float64, len(fc.swap))
	for k, v := range fc.swap {
		cp[k] = make(map[string][2]float64, len(v))
		for kk, vv := range v {
			cp[k][kk] = vv
		}
	}
	return cp
}

// TradeModes 返回 TTL 内新鲜的 trade_mode（canonical → venueKey → mode，D-060 F4）。
// 过期条目不出现 = 未知 → 消费方放行（检测器过滤/执行预检同口径）。
func (fc *FactCache) TradeModes() map[string]map[string]int64 {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	now := time.Now()
	cp := make(map[string]map[string]int64, len(fc.tradeMode))
	for canon, vm := range fc.tradeMode {
		for vk, e := range vm {
			if now.Sub(e.ts) > fc.tradeModeTTL {
				continue
			}
			if cp[canon] == nil {
				cp[canon] = make(map[string]int64, len(vm))
			}
			cp[canon][vk] = e.mode
		}
	}
	return cp
}

// TradeMode 查单腿 trade_mode（F5 执行预检用；TTL 过期/未知 → ok=false 放行）。
func (fc *FactCache) TradeMode(canonical, venueKey string) (int64, bool) {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	e, ok := fc.tradeMode[canonical][venueKey]
	if !ok || time.Since(e.ts) > fc.tradeModeTTL {
		return 0, false
	}
	return e.mode, true
}

// Engine 事件驱动管线：QuoteBus 报价事件 → 检测 → 评估 → 存储。
// commissionFor 按 venueKey（"{venue}:{venue_type}"）解析单腿佣金（bps/腿），D-036 per-venue 真实定价，
// 由 main 注入：CFD → broker_accounts.commission_bps（NULL 回退 config 默认），CEX → 逐所 taker 回退默认。
type Engine struct {
	bus           *bus.QuoteBus
	detectors     []detector.Detector
	evalCfg       evaluator.Config
	store         opportunityStore
	facts         *FactCache
	commissionFor func(venueKey string) float64
	scanThrottle  time.Duration
	// oppTopic 新机会站内推送总线（D-050）：仅新插入且净收益 ≥ 门槛时发布。nil = 未接线（测试可不接）。
	oppTopic *bus.Topic[pgstore.Opportunity]
	// minNotifyNetBps 机会推送提醒门槛（bps）；0 = 全部新机会都提醒（config.ARB_NOTIFY_MIN_NET_BPS）。
	minNotifyNetBps float64
	// minStoreNetBps CROSS_VENUE 入库最低净收益（bps）；D-078 round-trip 修正：
	// 低于此值的价差型机会不入库（从源头消除垃圾）。仅适用于 CROSS_VENUE（net 为一次性 bps）；
	// 息差型 net 为年化百分点，由检测器 MinNetAnnualized 门槛过滤。
	minStoreNetBps float64
	// excludeCanonicals 检测产出排除集合（D-072 资金提醒弹窗提示词 B1：复用 ARB_AUTOTRADE_EXCLUDE_CANONICALS
	// 双用途——执行层排除 + 检测层排除，P3 单一真相）。nil/空 = 不排除（测试默认）。
	// 命中时机会不入库（TTL 自然过期不适用——根本不产），从源头消除 BRENT/WTI 假机会（1314615 报价偏高 $3，D-068 实证）。
	excludeCanonicals map[string]bool

	mu       sync.Mutex
	lastScan map[string]time.Time // canonical → 上次扫描时间（节流）
}

// New 创建引擎。commissionFor 必填（成本正确性依赖真实佣金，D-036），nil = 装配错误直接 panic。
// oppTopic 允许 nil（测试不接推送）；minNotifyNetBps 为提醒门槛（缺省 0 = 所有新机会都推）。
// minStoreNetBps 为 CROSS_VENUE 入库门槛（D-078，缺省 0 = 不过滤）。
func New(b *bus.QuoteBus, detectors []detector.Detector, cfg evaluator.Config, store opportunityStore, fc *FactCache, commissionFor func(venueKey string) float64, scanThrottle time.Duration, oppTopic *bus.Topic[pgstore.Opportunity], minNotifyNetBps, minStoreNetBps float64) *Engine {
	if commissionFor == nil {
		panic("engine: commissionFor resolver required (D-036 per-venue 佣金)")
	}
	if scanThrottle <= 0 {
		scanThrottle = 30 * time.Second
	}
	return &Engine{
		bus:             b,
		detectors:       detectors,
		evalCfg:         cfg,
		store:           store,
		facts:           fc,
		commissionFor:   commissionFor,
		scanThrottle:    scanThrottle,
		oppTopic:        oppTopic,
		minNotifyNetBps: minNotifyNetBps,
		minStoreNetBps:  minStoreNetBps,
		lastScan:        make(map[string]time.Time),
	}
}

// SetExcludeCanonicals 注入检测产出排除集合（D-072 资金提醒弹窗提示词 B1）。
// 复用 ARB_AUTOTRADE_EXCLUDE_CANONICALS（执行层+检测层双用途，P3 单一真相）；nil/空 = 不排除。
// main 在 config 加载后注入；命中 canonical 的机会不入库（从源头消除假机会）。
func (e *Engine) SetExcludeCanonicals(set map[string]bool) {
	e.excludeCanonicals = set
}

// Run 订阅 QuoteBus 并驱动检测→评估→存储循环。阻塞至 ctx 取消。
func (e *Engine) Run(ctx context.Context) {
	ch, cancel := e.bus.Subscribe("")
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case q, ok := <-ch:
			if !ok {
				return
			}
			e.onQuote(ctx, q)
		}
	}
}

// onQuote 处理单条报价事件：节流 → 快照 → 检测 → 评估 → 存储。
func (e *Engine) onQuote(ctx context.Context, q bus.Quote) {
	// 节流：每 canonical 一个扫描周期（config ARB_SCAN_THROTTLE，D-036 去硬编码）
	e.mu.Lock()
	last := e.lastScan[q.Symbol]
	if time.Since(last) < e.scanThrottle {
		e.mu.Unlock()
		return
	}
	e.lastScan[q.Symbol] = time.Now()
	e.mu.Unlock()

	// 新鲜度检查（§5.0）
	if time.Since(q.Time) > e.evalCfg.DetectorFreshnessTTL {
		return
	}

	snapshot := e.bus.Snapshot(ctx, []string{q.Symbol})
	candidates := detector.ScanAll(e.detectors, snapshot, e.facts)

	for _, c := range candidates {
		result := e.evaluate(ctx, c, snapshot)
		if result == nil {
			continue
		}
		if result.Rating == evaluator.RatingTrap {
			continue
		}
		o := resultToOpp(result)
		// D-072 资金提醒弹窗提示词 B1：检测层排除 BRENT/WTI 等假机会（复用 ARB_AUTOTRADE_EXCLUDE_CANONICALS，
		// 执行层+检测层双用途，P3 单一真相）。命中 → 不入库（从源头消除，TTL 自然过期不适用）。
		if len(e.excludeCanonicals) > 0 && e.excludeCanonicals[o.Canonical] {
			continue
		}
		// D-078 round-trip 入库门槛：CROSS_VENUE net 为一次性 bps，低于门槛不入库（从源头消除垃圾）。
		// 息差型 net 为年化百分点，不适用此 bps 门槛（由检测器 MinNetAnnualized 过滤）。
		if o.Type == "CROSS_VENUE" && o.NetBps < e.minStoreNetBps {
			continue
		}
		inserted, err := e.store.UpsertOpportunity(ctx, o)
		if err != nil {
			log.Printf("[engine] upsert opportunity %s: %v", result.ID, err)
			continue
		}
		// D-050 站内推送：仅新插入且净收益 ≥ 提醒门槛时发布；刷新/低净值不推（30s 快照兜底）。
		if e.oppTopic != nil && inserted && o.NetBps >= e.minNotifyNetBps {
			e.oppTopic.Publish(*o)
		}
	}
}

func (e *Engine) evaluate(ctx context.Context, c detector.Candidate, snapshot map[string]map[string]bus.Quote) *evaluator.Result {
	// 查买/卖腿报价获取 spread
	buyQ := findQuote(snapshot, c.Canonical, c.VenueBuy)
	sellQ := findQuote(snapshot, c.Canonical, c.VenueSell)
	if buyQ == nil || sellQ == nil {
		return nil
	}

	spreadBuy := (buyQ.Ask - buyQ.Bid) / 2
	spreadSell := (sellQ.Ask - sellQ.Bid) / 2

	// 佣金（bps/腿，D-036 per-venue 解析：CFD 账号真实值/CEX 逐所 taker，均回退 config 默认）
	commissionBps := e.commissionFor(c.VenueBuy) + e.commissionFor(c.VenueSell)

	// 息差型日息差由评估器从净年化推导，无需再取 swap Fact（D-036 简化死参数）
	return evaluator.Evaluate(c, e.evalCfg, spreadBuy, spreadSell, commissionBps, 0)
}

func findQuote(snapshot map[string]map[string]bus.Quote, canonical, venueKey string) *bus.Quote {
	venues, ok := snapshot[canonical]
	if !ok {
		return nil
	}
	// venueKey 格式 "{venue}:{venueType}"
	for _, q := range venues {
		if q.Venue+":"+q.VenueType == venueKey {
			return &q
		}
	}
	return nil
}

func resultToOpp(r *evaluator.Result) *pgstore.Opportunity {
	return &pgstore.Opportunity{
		ID:             r.ID,
		Type:           r.Type,
		Canonical:      r.Canonical,
		VenueBuy:       r.VenueBuy,
		VenueSell:      r.VenueSell,
		PriceBuy:       r.PriceBuy,
		PriceSell:      r.PriceSell,
		GrossBps:       r.GrossBps,
		SpreadCost:     r.SpreadCost,
		CommissionCost: r.CommissionCost,
		SlippageCost:   r.SlippageCost,
		SwapCost:       r.SwapCost,
		NetBps:         r.NetBps,
		BreakevenDays:  r.BreakevenDays,
		QuoteTime:      r.QuoteTime,
		ExpiresAt:      r.ExpiresAt,
	}
}
