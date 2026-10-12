// Package bus 提供内存报价总线（§4.1）。
// drain-then-replace 策略保证慢消费者丢旧保新。
package bus

import (
	"context"
	"sync"
	"time"

	"arb/internal/data/keydomain"
)

// quoteSub D-249 S2：订阅条目（D-108 Topic 同款 close/send 竞态防护）。
// 每个 sub 独立 mutex 同步 close 与 send，cancel 用 sync.Once 保护 close（重复 cancel 不 panic），
// Publish 侧 send 前持 sub.mu 检查 closed 标志（race-clean，不依赖 recover）。
type quoteSub struct {
	ch     chan Quote
	mu     sync.Mutex
	closed bool
	once   sync.Once
}

// QuoteBus 是报价 pub/sub 中心（§4.1）。
// latest 按 canonical → "venue:venueType" → Quote 组织，
// 避免同 venue 现货/永续互相覆盖（D-026）。
type QuoteBus struct {
	mu          sync.RWMutex
	subscribers map[string][]*quoteSub
	latestMu    sync.RWMutex
	latest      map[string]map[string]Quote
	// freeze guard：lastChange 记录 (canonical, venueKey) 上次「价格值变化」时刻。
	// 语义：首见不算变化（无条目=从未变化）；同值心跳不更新；价格值变化才记 time.Now()。
	// 与 latest 同锁（latestMu），Publish 内原子更新。
	lastChange map[string]map[string]time.Time
	// 方案A：lastPulse 记录 (canonical, venueKey) 上次「活性脉冲」时刻——
	// mark-price/index-tickers 帧到即刷（簿面静止期的订阅活证据，不写报价值）。
	// 与 latest 同锁，Touch 内原子更新；eval 域零消费（quote.Time 语义不变）。
	lastPulse map[string]map[string]time.Time
}

// New 创建 QuoteBus。
func New() *QuoteBus {
	return &QuoteBus{
		subscribers: make(map[string][]*quoteSub),
		latest:      make(map[string]map[string]Quote),
		lastChange:  make(map[string]map[string]time.Time),
		lastPulse:   make(map[string]map[string]time.Time),
	}
}

// Subscribe 返回接收指定 canonical symbol 报价的 channel。
// 返回的 cancel func 取消订阅并关闭 channel（sync.Once 保护重复 cancel 不 panic）。
func (b *QuoteBus) Subscribe(symbol string) (<-chan Quote, func()) {
	sub := &quoteSub{ch: make(chan Quote, 1)}
	b.mu.Lock()
	b.subscribers[symbol] = append(b.subscribers[symbol], sub)
	b.mu.Unlock()
	ch := sub.ch
	return ch, func() {
		sub.once.Do(func() {
			sub.mu.Lock()
			sub.closed = true
			close(sub.ch)
			sub.mu.Unlock()
		})
		b.mu.Lock()
		subs := b.subscribers[symbol]
		for i, c := range subs {
			if c.ch == ch {
				b.subscribers[symbol] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
	}
}

// Publish 发布报价到所有订阅该 canonical symbol 的 channel。
// drain-then-replace：channel 满时丢旧保新。
// send 前持 sub.mu 检查 closed（与 cancel 的 close 同步，race-clean）；
// 复制 subs 切片避免共享底层数组并发 append 竞争（D-108 Topic 同款）。
func (b *QuoteBus) Publish(q Quote) {
	b.latestMu.Lock()
	vk := q.VenueKey()
	if b.latest[q.Symbol] == nil {
		b.latest[q.Symbol] = make(map[string]Quote)
	}
	// freeze guard：首见不算变化；仅当存在上一值且 bid/ask 之一变化才记 lastChangeAt。
	// 心跳报价（时间戳新但价格冻结）不会更新 lastChange → 执行侧预检据此识破骗线。
	if prev, hasPrev := b.latest[q.Symbol][vk]; hasPrev && (prev.Bid != q.Bid || prev.Ask != q.Ask) {
		if b.lastChange[q.Symbol] == nil {
			b.lastChange[q.Symbol] = make(map[string]time.Time)
		}
		b.lastChange[q.Symbol][vk] = time.Now()
	}
	b.latest[q.Symbol][vk] = q
	b.latestMu.Unlock()

	b.mu.RLock()
	symSubs := b.subscribers[q.Symbol]
	wildSubs := b.subscribers[""]
	subs := make([]*quoteSub, 0, len(symSubs)+len(wildSubs))
	subs = append(subs, symSubs...)
	subs = append(subs, wildSubs...)
	b.mu.RUnlock()
	for _, sub := range subs {
		sub.mu.Lock()
		if sub.closed {
			sub.mu.Unlock()
			continue
		}
		select {
		case sub.ch <- q:
		default:
			select {
			case <-sub.ch:
			default:
			}
			select {
			case sub.ch <- q:
			default:
			}
		}
		sub.mu.Unlock()
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

// EvictVenue 驱逐某裸 venue 的全部最新报价与 lastChange 记录（D-150）——账号删除后
// qbus 内存态不残留：否则 mt-health 的 CEX 兜底收录径（quoteAges \ broker_accounts）
// 把已删账号当 CEX venue 持续监控，报价年龄单调涨→crit 告警每 30min 重发。
// 返回驱逐的报价条数（观测用）。
func (b *QuoteBus) EvictVenue(venue string) int {
	b.latestMu.Lock()
	defer b.latestMu.Unlock()
	n := 0
	for sym, venues := range b.latest {
		for vk := range venues {
			if keydomain.BareOf(vk) == venue {
				delete(venues, vk)
				n++
			}
		}
		if len(venues) == 0 {
			delete(b.latest, sym)
		}
	}
	for sym, venues := range b.lastChange {
		for vk := range venues {
			if keydomain.BareOf(vk) == venue {
				delete(venues, vk)
			}
		}
		if len(venues) == 0 {
			delete(b.lastChange, sym)
		}
	}
	for sym, venues := range b.lastPulse { // D-155：脉冲记录随 venue 同驱逐
		for vk := range venues {
			if keydomain.BareOf(vk) == venue {
				delete(venues, vk)
			}
		}
		if len(venues) == 0 {
			delete(b.lastPulse, sym)
		}
	}
	return n
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

// Touch D-155 方案A：活性脉冲打点（mark-price/index-tickers 帧到即调）——
// 簿面静止时 tickers 帧稀疏，脉冲证明该键订阅仍活；只刷时刻不写报价值。
func (b *QuoteBus) Touch(symbol, venueKey string) {
	b.latestMu.Lock()
	defer b.latestMu.Unlock()
	if b.lastPulse[symbol] == nil {
		b.lastPulse[symbol] = make(map[string]time.Time)
	}
	b.lastPulse[symbol][venueKey] = time.Now()
}

// PulseAt 返回 (canonical, venueKey) 上次活性脉冲时刻（ok=false=从未有脉冲）。
func (b *QuoteBus) PulseAt(symbol, venueKey string) (time.Time, bool) {
	b.latestMu.RLock()
	defer b.latestMu.RUnlock()
	if venues, found := b.lastPulse[symbol]; found {
		if t, ok := venues[venueKey]; ok {
			return t, true
		}
	}
	return time.Time{}, false
}

// LastChangeAt 返回 (canonical, venueKey) 上次价格值变化的时刻（D-155 freeze guard）。
// ok=false = 该腿从未发生过价格变化（首见/仅心跳）→ 调用方应判为冻结（fail-closed）。
// ok=true  = t 为最近一次 bid/ask 变化时刻，调用方按窗口判定 now.Sub(t) 是否超期。
func (b *QuoteBus) LastChangeAt(symbol, venueKey string) (t time.Time, ok bool) {
	b.latestMu.RLock()
	defer b.latestMu.RUnlock()
	if venues, found := b.lastChange[symbol]; found {
		if t, ok = venues[venueKey]; ok {
			return t, true
		}
	}
	return time.Time{}, false
}
