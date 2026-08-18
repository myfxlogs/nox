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

// FactCache 实现 detector.FactProvider，从内存缓存提供 funding/swap Fact。
type FactCache struct {
	mu      sync.RWMutex
	funding map[string]map[string]float64          // canonical → venue → 年化
	swap    map[string]map[string][2]float64        // canonical → venue → [long, short]
}

func NewFactCache() *FactCache {
	return &FactCache{
		funding: make(map[string]map[string]float64),
		swap:    make(map[string]map[string][2]float64),
	}
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

// Engine 事件驱动管线：QuoteBus 报价事件 → 检测 → 评估 → 存储。
// commissionFor 按 venueKey（"{venue}:{venue_type}"）解析单腿佣金（bps/腿），D-036 per-venue 真实定价，
// 由 main 注入：CFD → broker_accounts.commission_bps（NULL 回退 config 默认），CEX → 逐所 taker 回退默认。
type Engine struct {
	bus           *bus.QuoteBus
	detectors     []detector.Detector
	evalCfg       evaluator.Config
	store         *pgstore.Store
	facts         *FactCache
	commissionFor func(venueKey string) float64
	scanThrottle  time.Duration

	mu       sync.Mutex
	lastScan map[string]time.Time // canonical → 上次扫描时间（节流）
}

// New 创建引擎。commissionFor 必填（成本正确性依赖真实佣金，D-036），nil = 装配错误直接 panic。
func New(b *bus.QuoteBus, detectors []detector.Detector, cfg evaluator.Config, store *pgstore.Store, fc *FactCache, commissionFor func(venueKey string) float64, scanThrottle time.Duration) *Engine {
	if commissionFor == nil {
		panic("engine: commissionFor resolver required (D-036 per-venue 佣金)")
	}
	if scanThrottle <= 0 {
		scanThrottle = 30 * time.Second
	}
	return &Engine{
		bus:           b,
		detectors:     detectors,
		evalCfg:       cfg,
		store:         store,
		facts:         fc,
		commissionFor: commissionFor,
		scanThrottle:  scanThrottle,
		lastScan:      make(map[string]time.Time),
	}
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
		if err := e.store.UpsertOpportunity(ctx, resultToOpp(result)); err != nil {
			log.Printf("[engine] upsert opportunity %s: %v", result.ID, err)
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
