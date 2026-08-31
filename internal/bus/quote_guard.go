// Package bus 提供 QuoteGuard——报价量级校验（D-177）。
// 监听 QuoteBus，对新 (canonical, venue) 组合的首条报价做量级比对。
// 比同 canonical 其他 venue 的 mid，比值 >1.3 → 删映射 + ReloadSymbolMap 刷新 adapter 内存。
package bus

import (
	"context"
	"log"
	"math"
	"sort"
	"sync"
)

// SymbolMappingStore quoteGuard 所需的 store 接口（避免直接依赖 pgstore）。
type SymbolMappingStore interface {
	DeleteSymbolMapping(ctx context.Context, venue, brokerSymbol string) error
}

// QuoteGuard 监听 QuoteBus，对新 (canonical, venue) 组合的首条报价做量级校验。
// 比对同 canonical 其他 venue 的 mid，比值 >1.3 → 删映射 + ReloadSymbolMap + 告警。
// 不取消订阅（MT5 adapter 未封装 Unsubscribe RPC；ReloadSymbolMap 后 canonical() 回退 brokerSym 本身，
// QuoteBus.Publish 仍发但 Symbol=brokerSym 不匹配任何 canonical 订阅者 → 自然丢弃）。
type QuoteGuard struct {
	bus    *QuoteBus
	store  SymbolMappingStore
	reload func(ctx context.Context, venue string) // BrokerManager.ReloadSymbolMap
	seen   map[string]bool                         // "canonical|venueKey" → 已校验通过/已隔离
	mu     sync.Mutex
}

// NewQuoteGuard 创建 QuoteGuard。reload = BrokerManager.ReloadSymbolMap（删映射后刷新 adapter 内存）。
func NewQuoteGuard(bus *QuoteBus, store SymbolMappingStore, reload func(ctx context.Context, venue string)) *QuoteGuard {
	return &QuoteGuard{bus: bus, store: store, reload: reload, seen: make(map[string]bool)}
}

// Run 订阅 QuoteBus wildcard（""）校验全部 canonical 报价。
// 用 wildcard 而非 sampleCanonicals 快照——新 broker 通过 AddBroker 接入后新增的 canonical
// 也会被覆盖（sampleCanonicals 是启动快照，漏掉运行时新增 canonical = 防线缺口）。
// 阻塞至 ctx 取消。
func (g *QuoteGuard) Run(ctx context.Context) {
	ch, cancel := g.bus.Subscribe("")
	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case q, ok := <-ch:
				if !ok {
					return
				}
				g.onQuote(ctx, q)
			}
		}
	}()
	<-ctx.Done()
}

func (g *QuoteGuard) onQuote(ctx context.Context, q Quote) {
	key := q.Symbol + "|" + q.VenueKey()
	g.mu.Lock()
	if g.seen[key] {
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()

	mid := q.Mid()
	if mid <= 0 {
		return
	}

	// 查同 canonical 其他 venue 的最新报价
	snapshot := g.bus.Snapshot(ctx, []string{q.Symbol})
	venues, ok := snapshot[q.Symbol]
	if !ok || len(venues) < 2 {
		g.mu.Lock()
		g.seen[key] = true
		g.mu.Unlock()
		return
	}

	var refMids []float64
	for vk, other := range venues {
		if vk == q.VenueKey() {
			continue
		}
		if other.Mid() > 0 {
			refMids = append(refMids, other.Mid())
		}
	}
	if len(refMids) == 0 {
		g.mu.Lock()
		g.seen[key] = true
		g.mu.Unlock()
		return
	}

	refMid := median(refMids)
	ratio := math.Max(mid/refMid, refMid/mid)
	if ratio <= 1.3 {
		g.mu.Lock()
		g.seen[key] = true
		g.mu.Unlock()
		return
	}

	// 量级不符 → 删映射 + ReloadSymbolMap 刷新 adapter 内存 + 告警
	log.Printf("[quoteGuard] %s/%s → %s 量级不符(%.4f vs ref %.4f, ratio %.1f)，自动隔离",
		q.Venue, q.VenueSymbol, q.Symbol, mid, refMid, ratio)
	if err := g.store.DeleteSymbolMapping(ctx, q.Venue, q.VenueSymbol); err != nil {
		log.Printf("[quoteGuard] delete mapping %s/%s: %v", q.Venue, q.VenueSymbol, err)
	}
	// C-1 修正：必须调 ReloadSymbolMap 刷新 adapter 内存 symMap（atomic.Pointer），
	// 否则 canonical() 仍返回错误 canonical → 报价仍以错误 canonical 发布 → 隔离无效。
	if g.reload != nil {
		g.reload(ctx, q.Venue)
	}
	// C-2 修正：标记 seen——避免 ReloadSymbolMap 生效延迟期间（毫秒级）的残余报价重复触发 log 刷屏。
	g.mu.Lock()
	g.seen[key] = true
	g.mu.Unlock()
}

func median(vals []float64) float64 {
	sort.Float64s(vals)
	n := len(vals)
	if n%2 == 1 {
		return vals[n/2]
	}
	return (vals[n/2-1] + vals[n/2]) / 2
}
