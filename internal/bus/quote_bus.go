// Package bus 提供内存报价总线（§4.1）。
// drain-then-replace 策略保证慢消费者丢旧保新。
package bus

import (
	"context"
	"sync"
)

// QuoteBus 是报价 pub/sub 中心（§4.1）。
// latest 按 canonical → "venue:venueType" → Quote 组织，
// 避免同 venue 现货/永续互相覆盖（D-026）。
type QuoteBus struct {
	mu          sync.RWMutex
	subscribers map[string][]chan Quote
	latestMu    sync.RWMutex
	latest      map[string]map[string]Quote
}

// New 创建 QuoteBus。
func New() *QuoteBus {
	return &QuoteBus{
		subscribers: make(map[string][]chan Quote),
		latest:      make(map[string]map[string]Quote),
	}
}

// Subscribe 返回接收指定 canonical symbol 报价的 channel。
// 返回的 cancel func 取消订阅并关闭 channel。
func (b *QuoteBus) Subscribe(symbol string) (<-chan Quote, func()) {
	ch := make(chan Quote, 1)
	b.mu.Lock()
	b.subscribers[symbol] = append(b.subscribers[symbol], ch)
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		subs := b.subscribers[symbol]
		for i, c := range subs {
			if c == ch {
				b.subscribers[symbol] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		close(ch)
	}
}

// Publish 发布报价到所有订阅该 canonical symbol 的 channel。
// drain-then-replace：channel 满时丢旧保新。
func (b *QuoteBus) Publish(q Quote) {
	b.latestMu.Lock()
	vk := q.VenueKey()
	if b.latest[q.Symbol] == nil {
		b.latest[q.Symbol] = make(map[string]Quote)
	}
	b.latest[q.Symbol][vk] = q
	b.latestMu.Unlock()

	b.mu.RLock()
	chs := append(b.subscribers[q.Symbol], b.subscribers[""]...)
	b.mu.RUnlock()
	for _, ch := range chs {
		select {
		case ch <- q:
		default:
			select { case <-ch: default: }
			select { case ch <- q: default: }
		}
	}
}

// Snapshot 返回指定 canonical symbols 的最新报价快照：
// canonical → "venue:venueType" → Quote。
// 从未收到报价的 symbol 不出现在结果中。
func (b *QuoteBus) Snapshot(_ context.Context, symbols []string) map[string]map[string]Quote {
	b.latestMu.RLock()
	defer b.latestMu.RUnlock()
	result := make(map[string]map[string]Quote, len(symbols))
	for _, sym := range symbols {
		if venues, ok := b.latest[sym]; ok {
			cp := make(map[string]Quote, len(venues))
			for k, v := range venues {
				cp[k] = v
			}
			result[sym] = cp
		}
	}
	return result
}

// Latest 返回某 canonical symbol 某 venue 的最新报价。
// 不存在返回 zero Quote 和 false。
func (b *QuoteBus) Latest(symbol, venueKey string) (Quote, bool) {
	b.latestMu.RLock()
	defer b.latestMu.RUnlock()
	if venues, ok := b.latest[symbol]; ok {
		if q, ok2 := venues[venueKey]; ok2 {
			return q, true
		}
	}
	return Quote{}, false
}

// AllLatest 返回全部最新报价的扁平列表（供快照类 RPC 用）。
func (b *QuoteBus) AllLatest() []Quote {
	b.latestMu.RLock()
	defer b.latestMu.RUnlock()
	var out []Quote
	for _, venues := range b.latest {
		for _, q := range venues {
			out = append(out, q)
		}
	}
	return out
}
