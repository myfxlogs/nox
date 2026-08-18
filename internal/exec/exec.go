// Package exec 定义执行层接口（§13.2）。
// 每个市场一个 Executor 实例，按 Key() = "venue:venueType" 路由。
package exec

import (
	"context"
	"sync"
	"time"
)

// Order 是下单请求（市价单）。
type Order struct {
	Symbol    string  // venue 原始符号
	Side      string  // BUY / SELL
	Qty       float64 // MT: lots; CEX: base qty
	Venue     string
	VenueType string
}

// Fill 是成交回执。
type Fill struct {
	OrderID    string
	Price      float64
	Qty        float64
	Swap       float64 // MT 平仓时 swap（开仓时通常 0）
	Commission float64
	Ts         time.Time
}

// OrderRef 是平仓引用。
type OrderRef struct {
	OrderID string
	Venue   string
	Symbol  string
}

// Executor 是执行器接口（§13.2）。
type Executor interface {
	Key() string // "venue:venueType"
	PlaceOrder(ctx context.Context, o Order) (Fill, error)
	CloseOrder(ctx context.Context, ref OrderRef) (Fill, error)
}

// Registry 按 Key 路由执行器。动态启停（Broker 管理器）后并发读写，须加锁。
type Registry struct {
	mu        sync.RWMutex
	executors map[string]Executor
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{executors: make(map[string]Executor)}
}

// Register 注册执行器。
func (r *Registry) Register(e Executor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executors[e.Key()] = e
}

// Unregister 注销执行器（RemoveBroker 时清理，键不存在时静默）。
func (r *Registry) Unregister(venueKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.executors, venueKey)
}

// Get 按 venueKey 返回执行器，不存在返回 false。
func (r *Registry) Get(venueKey string) (Executor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.executors[venueKey]
	return e, ok
}
