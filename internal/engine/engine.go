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
type Engine struct {
	bus       *bus.QuoteBus
	detectors []detector.Detector
	evalCfg   evaluator.Config
	store     *pgstore.Store
	facts     *FactCache

	mu       sync.Mutex
	lastScan map[string]time.Time // canonical → 上次扫描时间（节流）
}

// New 创建引擎。
func New(b *bus.QuoteBus, detectors []detector.Detector, cfg evaluator.Config, store *pgstore.Store, fc *FactCache) *Engine {
	return &Engine{
		bus:       b,
		detectors: detectors,
		evalCfg:   cfg,
		store:     store,
		facts:     fc,
		lastScan:  make(map[string]time.Time),
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
	// 节流：每 canonical 100ms（D-026）
	e.mu.Lock()
	last := e.lastScan[q.Symbol]
	if time.Since(last) < 100*time.Millisecond {
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

	// 佣金：CFD ~0.3 bps/腿，CEX taker 0.1%/腿 = 10 bps/腿
	commissionBps := commissionFor(c.VenueBuy) + commissionFor(c.VenueSell)

	// swap bps（息差型从 FactCache 取）
	var swapBps float64
	if c.Type == detector.TypeCarry || c.Type == detector.TypeFundingHedge {
		swapBps = e.facts.SwapFacts()[c.Canonical][c.VenueBuy][0] // long leg swap
	}

	return evaluator.Evaluate(c, e.evalCfg, spreadBuy, spreadSell, commissionBps, swapBps, 0)
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

func commissionFor(venueKey string) float64 {
	// 简化：CEX taker 10 bps/腿，CFD 0.3 bps/腿
	if contains(venueKey, "binance") || contains(venueKey, "okx") {
		return 10.0
	}
	return 0.3
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
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
