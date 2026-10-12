// cex_event_cache.go —— D-450 S3：CEX 私有 WS 事件缓存层（加速器，非真相源）。
// 两条边界铁律：
//  1. 缓存只存「最近一次事件」的完整快照/终态，不做多事件增量合并；
//  2. 命中窗口外一律回落 REST——REST 轮询双轨不拆，事件只缩短感知延迟。
package exchange

import (
	"context"
	"sync"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/venue"
)

// EventFreshTTL 订单事件新鲜窗（SSOT：≤5s 内命中直返）。
const EventFreshTTL = 5 * time.Second

// PositionFreshTTL 持仓集合事件新鲜窗（SSOT：≤10s）。
const PositionFreshTTL = 10 * time.Second

// OrderEvent 订单终态/中间态事件快照（gate futures.orders / binance ORDER_TRADE_UPDATE 归一）。
type OrderEvent struct {
	State      string // filled / partial / canceled / live（与 OrderStatusQuerier 归一值同域）
	AccFillQty float64
	AvgPx      float64
	FeeUSD     float64
	At         time.Time
}

// OrderEventCache venueKey → orderKey → 最近事件。orderID 与 clOrdID 各立一键（双键同值写）。
type OrderEventCache struct {
	mu     sync.RWMutex
	m      map[string]map[string]OrderEvent
	onPut  func(venueKey string) // D-469a S2：写面 kick 回调（setter 注入，nil 安全）
	subs   map[int]chan string   // D-018 R1：写面事件订阅扇出（maker 等待唤醒；payload=venueKey）
	subSeq int
}

// SetOnPut D-469a S2：注入写面 kick 回调（先例=SwapSnapshotStore setter 注入，
// 避免构造签名连锁变更）。回调在锁外触发。
func (c *OrderEventCache) SetOnPut(fn func(venueKey string)) { c.onPut = fn }

// NewOrderEventCache 创建共享缓存（进程级单例，由各 venue 流写、CachedOrderStatusQuerier 读）。
func NewOrderEventCache() *OrderEventCache {
	return &OrderEventCache{m: map[string]map[string]OrderEvent{}, subs: map[int]chan string{}}
}

// SubscribeEvents D-018 R1：订单事件订阅（maker 等待唤醒用——WS 帧写缓存即扇出
// venueKey，订阅方据此回读快照，替代 REST 轮询=D-004 数据流正道）。返回数据通道与
// 取消函数（订阅者侧 defer 取消防 goroutine 泄漏）。无订阅者零开销。
func (c *OrderEventCache) SubscribeEvents() (<-chan string, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan string, 16)
	id := c.subSeq
	c.subSeq++
	c.subs[id] = ch
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, ok := c.subs[id]; ok {
			delete(c.subs, id)
			close(ch)
		}
	}
}

// PutOrder 写事件：orderID/clOrdID 非空各建一键（消费面两键都可能查）。
func (c *OrderEventCache) PutOrder(venueKey, orderID, clOrdID string, ev OrderEvent) {
	c.mu.Lock()
	vm := c.m[venueKey]
	if vm == nil {
		vm = map[string]OrderEvent{}
		c.m[venueKey] = vm
	}
	if orderID != "" {
		vm[orderID] = ev
	}
	if clOrdID != "" {
		vm[clOrdID] = ev
	}
	c.mu.Unlock()
	if c.onPut != nil {
		c.onPut(venueKey)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, ch := range c.subs {
		select {
		case ch <- venueKey:
		default: // 订阅方积压=丢拍无害（快照判定以缓存为准，拍只是唤醒信号）
		}
	}
}

// get 命中且新鲜 → 返回事件（now 注入便于测试）。
func (c *OrderEventCache) get(venueKey, orderID, clOrdID string, ttl time.Duration, now time.Time) (OrderEvent, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	vm := c.m[venueKey]
	for _, k := range [2]string{orderID, clOrdID} {
		if k == "" {
			continue
		}
		if ev, ok := vm[k]; ok && now.Sub(ev.At) <= ttl {
			return ev, true
		}
	}
	return OrderEvent{}, false
}

// PeekOrder D-569 包B S4：订单事件薄包装读口（EventFreshTTL 复用，miss/stale→false）。
// 签名钉死——D-566/D-570 包B 复用本定义，禁改。
func (c *OrderEventCache) PeekOrder(venueKey, orderID string) (OrderEvent, bool) {
	return c.get(venueKey, orderID, "", EventFreshTTL, time.Now())
}

// OrderStatusQuerier D-508 S7c：接口定义在消费方——缓存包装层收的是「订单级查单
// 能力」非 exec 具体型（exec.CEXExecutor/reconcile 同名接口结构满足，正向注入）。
type OrderStatusQuerier interface {
	QueryOrderStatus(ctx context.Context, orderID, clOrdID, symbol string) (state string, accFillQty, avgPx, feeUSD float64, err error)
}

// CachedOrderStatusQuerier 包装 REST querier：新鲜事件命中直返，miss/stale 回落 REST。
type CachedOrderStatusQuerier struct {
	venueKey string
	rest     OrderStatusQuerier
	cache    *OrderEventCache
	ttl      time.Duration // 测试可缩短；默认 EventFreshTTL
	now      func() time.Time
}

// NewCachedOrderStatusQuerier 创建包装查询器（rest=真相源兜底，不得 nil）。
func NewCachedOrderStatusQuerier(venueKey string, rest OrderStatusQuerier, cache *OrderEventCache) *CachedOrderStatusQuerier {
	return &CachedOrderStatusQuerier{venueKey: venueKey, rest: rest, cache: cache, ttl: EventFreshTTL, now: time.Now}
}

// QueryOrderStatus 实现 OrderStatusQuerier：缓存=加速器，REST=真相源。
func (q *CachedOrderStatusQuerier) QueryOrderStatus(ctx context.Context, orderID, clOrdID, symbol string) (string, float64, float64, float64, error) {
	if ev, ok := q.cache.get(q.venueKey, orderID, clOrdID, q.ttl, q.now()); ok {
		return ev.State, ev.AccFillQty, ev.AvgPx, ev.FeeUSD, nil
	}
	return q.rest.QueryOrderStatus(ctx, orderID, clOrdID, symbol)
}

// PositionEventCache venueKey → 最近一次持仓全量事件快照。
type PositionEventCache struct {
	mu    sync.RWMutex
	m     map[string]posSnapshot
	onPut func(venueKey string) // D-469a S2：写面 kick 回调（setter 注入，nil 安全）
}

// SetOnPut D-469a S2：注入写面 kick 回调（nil 安全，锁外触发）。
func (c *PositionEventCache) SetOnPut(fn func(venueKey string)) { c.onPut = fn }

type posSnapshot struct {
	orders []venue.Order
	at     time.Time
}

// NewPositionEventCache 创建共享持仓事件缓存。
func NewPositionEventCache() *PositionEventCache {
	return &PositionEventCache{m: map[string]posSnapshot{}}
}

// PutPositions 整体替换 venue 持仓快照（a.P/futures.positions 推送帧本身即全量数组，
// 不做增量合并——上次未出现的持仓按已平处理）。
func (c *PositionEventCache) PutPositions(venueKey string, orders []venue.Order, at time.Time) {
	c.mu.Lock()
	cp := append([]venue.Order(nil), orders...)
	c.m[venueKey] = posSnapshot{orders: cp, at: at}
	c.mu.Unlock()
	if c.onPut != nil {
		c.onPut(venueKey)
	}
}

// get 命中且新鲜 → 返回快照副本。
func (c *PositionEventCache) get(venueKey string, ttl time.Duration, now time.Time) ([]venue.Order, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.m[venueKey]
	if !ok || now.Sub(s.at) > ttl {
		return nil, false
	}
	return append([]venue.Order(nil), s.orders...), true
}

// CachedVenueQuerier 包装 REST venue.Querier：全量快照新鲜→直返，否则回落 REST。
type CachedVenueQuerier struct {
	rest  venue.Querier
	cache *PositionEventCache
	ttl   time.Duration
	now   func() time.Time
}

// NewCachedVenueQuerier 创建包装持仓查询器。
func NewCachedVenueQuerier(rest venue.Querier, cache *PositionEventCache) *CachedVenueQuerier {
	return &CachedVenueQuerier{rest: rest, cache: cache, ttl: PositionFreshTTL, now: time.Now}
}

// VenueKey 透传 REST querier 身份（撞键归一 #id 后缀随 rest）。
func (q *CachedVenueQuerier) VenueKey() string { return q.rest.VenueKey() }

// QueryOpenedOrders 实现 venue.Querier。
func (q *CachedVenueQuerier) QueryOpenedOrders(ctx context.Context) ([]venue.Order, error) {
	if orders, ok := q.cache.get(q.rest.VenueKey(), q.ttl, q.now()); ok {
		return orders, nil
	}
	return q.rest.QueryOpenedOrders(ctx)
}

// KickingSnapshotSink D-469a S3：余额快照 sink 包 kick wrapper——WS 推送落库透传后
// 调 kick（目标=acctMon.Kick()，账户监控事件叫醒）。写失败不 kick（无新数据）。
func NewKickingSnapshotSink(inner VenueSnapshotSink, kick func()) VenueSnapshotSink {
	return &kickingSnapshotSink{inner: inner, kick: kick}
}

type kickingSnapshotSink struct {
	inner VenueSnapshotSink
	kick  func()
}

func (k *kickingSnapshotSink) InsertVenueAccountSnapshots(ctx context.Context, snaps []pgstore.VenueAccountSnapshot) error {
	err := k.inner.InsertVenueAccountSnapshots(ctx, snaps)
	if err == nil && k.kick != nil {
		k.kick()
	}
	return err
}

// PrivateEventSink S4 审计表写口（*pgstore.Store 原生实现）。
type PrivateEventSink interface {
	InsertCEXPrivateEvent(ctx context.Context, venue, venueType, channel, eventKey string, payload []byte) error
}
