// Package exec 定义执行层接口（§13.2）。
// 每个市场一个 Executor 实例，按 Key() = "venue:venueType" 路由。
package exec

import (
	"context"
	"sync"
	"time"
)

// OrderComment 是 ARB 订单写入 broker 的 comment 标记（单一真相源）。
// MT4/MT5 下单统一携带，供对账器（internal/reconcile）判别订单是否为 ARB 源：
// `arb` 前缀 → ARB 源订单；其余（遗留 ca_/cx_/tri_+hex 格式、手工 UUID、空）→ 系统外单不追踪。
const OrderComment = "arb"

// Order 是下单请求（市价单）。
type Order struct {
	Symbol    string  // venue 原始符号
	Side      string  // BUY / SELL
	Qty       float64 // MT: lots; CEX: base qty
	Venue     string
	VenueType string
	// PositionSide 仅 OKX perp（long_short_mode 双向持仓）减仓时需要：显式指定减哪个
	// 仓位侧。开仓传空 → 按方向派生（buy→long / sell→short，D-059 实施修正② 实测）；
	// 平仓必须传原持仓侧（BUY 开的 long 用 SELL 平 → posSide=long 才减 long，否则
	// SELL+posSide=short 会新开空仓——真实生产 bug，实测双双持仓）。其他 venue/类型忽略。
	PositionSide string
	// PosID 关联持仓 ID（D-108 P2 T1：平仓被拒时 critical 告警 posID 挂接；0=开仓/无上下文不告警）。
	PosID int64
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

// OpenedOrder venue 侧在仓订单（D-077 修复⑩ G1 平仓后回读用）。
type OpenedOrder struct {
	Ticket string
	Symbol string
	Side   string // BUY / SELL
	Volume float64
}

// PositionVerifier 按 ticket 回读 venue 在仓订单（D-077 修复⑩ G1）。
// 实现者：reconcile.VenueQuerier 适配器（main 注入）。nil = 未接线（测试，跳过回读）。
type PositionVerifier interface {
	// QueryOpenedOrders 返回该 venue 的在仓订单列表（按 ticket 匹配残留腿）。
	QueryOpenedOrders(ctx context.Context, venueKey string) ([]OpenedOrder, error)
}

// ContractSizeResolver 查询单腿合约规格（D-077 修复⑪A M1 平仓 PnL 权威化）。
// MT 腿：broker_symbol_map.contract_size（服务器 SymbolInfo）；CEX 腿：CS=1（qty 即基础币）。
// nil = 未接线（测试，CS 回退 1.0——仅 CEX 场景正确，MT 场景 PnL 会有量级误差）。
type ContractSizeResolver interface {
	// ContractSize 返回 venueKey+venueSymbol 的合约规格。ok=false = 未知。
	ContractSize(venueKey, venueSymbol string) (float64, bool)
}

// HistoryPnL 历史单权威 PnL 查证结果（D-077 修复⑪A M1 回读校准）。
type HistoryPnL struct {
	Profit     float64 // 服务器计算的已实现盈亏（权威）
	Swap       float64
	Commission float64
	ClosePrice float64
}

// PnLCalibrator 按 ticket 回读历史单权威 PnL（D-077 修复⑪A M1 回读校准）。
// 实现者：reconcile.OrderHistoryQuerier 适配器（main 注入）。nil = 未接线（用同步路径公式）。
type PnLCalibrator interface {
	// QueryHistoryPnL 按 ticket 查近 fromDays 天历史单 PnL。返回 nil = 未找到。
	QueryHistoryPnL(ctx context.Context, venueKey, ticket string, fromDays int) (*HistoryPnL, error)
}

// FxResolver 计价币→USD 汇率查询（D-105 T2 平仓 PnL 计价币换算）。
// 实现者：*pgstore.Store（USDPer 方法同款）。nil = 未接线（非 USD 计价腿 fx 不可得 → 0+alert）。
type FxResolver interface {
	// USDPer 返回 1 单位 quoteCcy 折 USD 的汇率。ok=false = 换算不可得（调用方 fail-closed）。
	USDPer(ctx context.Context, quoteCcy string, at time.Time) (float64, bool)
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
