// Broker 管理器：运行时 MT4/MT5 连接的生命周期所有者（docs/broker管理器实现方案.md §3/§5）。
// 统一管理 DB 账号的适配器启停、报价订阅、执行器/对账注册，支持前端动态添加/删除。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/fact"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

// SymbolGroup 服务器符号 + broker 分组 + 合约规格（MT5 GroupName 顶层段；MT4 降级空组/合约 0）。
type SymbolGroup struct {
	Symbol       string
	Group        string
	ContractSize float64 // 0 = 未知（MT4 / MT5 未返回）
	TradeMode    int32   // MT5 ENUM_SYMBOL_TRADE_MODE 透传（D-060 F7 seed 判据；MT4=0 无此字段）
}

// running 一个运行中适配器的可停止资源。
type running struct {
	stop         func() // 停止报价订阅 + 适配器连接
	querier      reconcile.VenueQuerier
	symbols      func(ctx context.Context) ([]string, error)      // 拉取服务器符号表
	symbolGroups func(ctx context.Context) ([]SymbolGroup, error) // 符号表 + 分组（仅服务 BrokerSymbols 展示，按需拉一次）
	// volumeSpec 单符号最小手数（MT5 SymbolParams SymGroup.MinLots；MT4/未运行 = nil）。
	// D-061 量规格预检用；known=false = 未知，调用方放行（venue 服务端拒单兜底）。
	volumeSpec func(ctx context.Context, symbol string) (float64, bool, error)
	// volumeStep 单符号 volume step（D-070 qty 精度取整）；MT4/未运行 = nil。
	volumeStep func(ctx context.Context, symbol string) (float64, bool, error)
	// symbolParams 单符号权威合约规格（MT5 SymbolParams；MT4/未运行 = nil）。零信任：
	// 保存映射的合约校验数据只信 broker 自取，不信前端传值（D-042）。
	symbolParams func(ctx context.Context, symbol string) (float64, bool, error)
	setSymMap    func(map[string]string)                        // 运行时刷新 brokerSymbol → canonical
	resubscribe  func([]string)                                 // 运行时补订阅新符号（保存映射后生效，D-047 缺陷③）
	swap         func(ctx context.Context) ([]fact.Fact, error) // 轮询 swap facts（§3.6，D-036）

	// per-venue symbolGroups TTL 缓存（D-045 §3.2.1）：避免矩阵页全量拉取时 N 账号 × SymbolList+SymbolParamsMany
	// 直连 mtapi；缓存失效回退直连，失败不下发脏数据。
	sgMu      sync.Mutex
	sgCache   []SymbolGroup
	sgCacheAt time.Time
}

// staticQuerier 环境变量适配器的 querier（不被动态管理，boot 时与 DB venue 去重）。
type staticQuerier struct {
	venue string
	q     reconcile.VenueQuerier
	swap  func(ctx context.Context) ([]fact.Fact, error) // nil = 无 swap 源
}

// BrokerManager 管理 DB 中 broker_accounts 的动态适配器。
type BrokerManager struct {
	store *pgstore.Store
	qbus  *bus.QuoteBus
	reg   *exec.Registry

	mu      sync.Mutex
	running map[string]*running // venue → running
	static  []staticQuerier
	onQ     func([]reconcile.VenueQuerier) // querier 集合变更回调

	commMu     sync.Mutex
	commission map[string]float64 // venue → 账号真实佣金（bps/腿，D-036）；缺省回退 config 默认

	// 冷启动退避（m.mu 保护）：任一 venue 映射符号缺报价 → 重试直到全部流入；
	// 超过 coldStartBound 仍缺 → 降级接受部分（防病态 symbol 永久阻塞整轮轮询）。
	coldStartSince time.Time

	// symbolGroups TTL（D-045 §3.2.1）：由 config.SymbolsTTL 注入。
	symbolGroupsTTL time.Duration

	// SwapKick swap 轮询唤醒（D-060 F2 boot 竞态修复）：新 venue attach / 映射增删后立即补一轮，
	// 消灭「attach 晚于首轮 → 最长 Interval 无 swap 数据」盲窗。nil = 未接线（测试直接构造）。main 注入
	// func() { scheduler.Kick("mt_swap") }。
	SwapKick func()

	// MarginSink margin fact 下沉注入（D-099 事件驱动化）：同 SwapKick 注入模式，
	// wiring/main 注入，brokerMgr 不依赖 engine 包；startAdapter 构造 adapter 时透传（nil 安全）。
	MarginSink func(fact.Fact)

	// OrderEventSink 订单事件下沉注入（D-107 P1 事件驱动二阶段）：同 MarginSink 注入模式，
	// wiring/main 注入（sink = 发布到 orderTopic）；startMT5 透传至 adapter（nil 安全）。
	OrderEventSink func(bus.OrderEvent)
}

// NewBrokerManager 创建管理器。symbolsTTL 由调用方从 config.SymbolsTTL 注入。
func NewBrokerManager(store *pgstore.Store, qbus *bus.QuoteBus, reg *exec.Registry, symbolsTTL time.Duration) *BrokerManager {
	return &BrokerManager{
		store:           store,
		qbus:            qbus,
		reg:             reg,
		running:         make(map[string]*running),
		commission:      make(map[string]float64),
		symbolGroupsTTL: symbolsTTL,
	}
}

// SetQuerierHook 注册 querier 集合变更回调（main 接 reconciler.SetQueriers）。
func (m *BrokerManager) SetQuerierHook(f func([]reconcile.VenueQuerier)) {
	m.onQ = f
}

// AddStaticQuerier 登记环境变量适配器的 querier。
func (m *BrokerManager) AddStaticQuerier(venue string, q reconcile.VenueQuerier) {
	m.addStatic(venue, q, nil)
}

// AddStaticSwapSource 为环境变量适配器登记 swap 采集（§3.6，不留静默缺口）。
func (m *BrokerManager) AddStaticSwapSource(venue string, swap func(ctx context.Context) ([]fact.Fact, error)) {
	m.addStatic(venue, nil, swap)
}

func (m *BrokerManager) addStatic(venue string, q reconcile.VenueQuerier, swap func(ctx context.Context) ([]fact.Fact, error)) {
	m.mu.Lock()
	m.static = append(m.static, staticQuerier{venue: venue, q: q, swap: swap})
	m.mu.Unlock()
	m.syncQueriers()
}

// StartAll 启动 DB 中全部 status=connected 账号（boot 恢复）。
func (m *BrokerManager) StartAll(ctx context.Context) {
	accounts, err := m.store.ListBrokerAccounts(ctx)
	if err != nil {
		log.Printf("[broker] list accounts: %v", err)
		return
	}
	m.mu.Lock()
	staticVenues := make(map[string]bool, len(m.static))
	for _, s := range m.static {
		staticVenues[s.venue] = true
	}
	m.mu.Unlock()

	for _, a := range accounts {
		if a.Status != "connected" {
			continue
		}
		if staticVenues[a.Venue] {
			log.Printf("[broker] skip %s: env adapter occupies venue", a.Venue)
			continue
		}
		if err := m.startAdapter(ctx, &a); err != nil {
			log.Printf("[broker] start %s: %v", a.Venue, err)
		}
	}
}

// AddBroker 持久化账号并启动适配器。连接失败不落库成功状态。
func (m *BrokerManager) AddBroker(ctx context.Context, a *pgstore.BrokerAccount) error {
	a.Status = "connected"
	if err := m.store.InsertBrokerAccount(ctx, a); err != nil {
		return fmt.Errorf("insert broker: %w", err)
	}
	if err := m.startAdapter(ctx, a); err != nil {
		_ = m.store.SetBrokerAccountStatus(ctx, a.Venue, "failed")
		return err
	}
	return nil
}

// stopAdapter 停止运行中适配器并注销执行器/querier（不删 DB 记录，RemoveBroker/DisableBroker 复用）。
func (m *BrokerManager) stopAdapter(venue string) {
	m.mu.Lock()
	r := m.running[venue]
	delete(m.running, venue)
	m.mu.Unlock()

	if r != nil {
		r.stop()
		m.reg.Unregister(venue + ":mt4")
		m.reg.Unregister(venue + ":mt5")
		m.commMu.Lock()
		delete(m.commission, venue)
		m.commMu.Unlock()
		m.syncQueriers()
	}
}

// setCommission 登记账号真实佣金（nil 清除 → 回退 config 默认）。
func (m *BrokerManager) setCommission(venue string, bps *float64) {
	m.commMu.Lock()
	defer m.commMu.Unlock()
	if bps == nil {
		delete(m.commission, venue)
		return
	}
	m.commission[venue] = *bps
}

// SetCommission 运行时热更新账号佣金（D-036 SetBrokerCommission RPC 用）；nil 清除回退默认。
func (m *BrokerManager) SetCommission(venue string, bps *float64) {
	m.setCommission(venue, bps)
}

// CommissionBps 返回 venue 账号真实佣金（bps/腿）；ok=false 表示未配置，调用方回退 config 默认。
func (m *BrokerManager) CommissionBps(venue string) (float64, bool) {
	m.commMu.Lock()
	defer m.commMu.Unlock()
	bps, ok := m.commission[venue]
	return bps, ok
}

// RemoveBroker 停止适配器并删除 DB 记录与符号映射。
func (m *BrokerManager) RemoveBroker(ctx context.Context, venue string) error {
	m.stopAdapter(venue)
	if err := m.store.DeleteVenueSymbolMappings(ctx, venue); err != nil {
		return fmt.Errorf("delete symbol mappings: %w", err)
	}
	if err := m.store.DeleteBrokerAccount(ctx, venue); err != nil {
		return fmt.Errorf("delete broker account: %w", err)
	}
	log.Printf("[broker] removed %s", venue)
	return nil
}

// DisableBroker 停用账号（D-033）：停止适配器 + status=stopped，保留 DB 行与符号映射（启用可恢复）。
func (m *BrokerManager) DisableBroker(ctx context.Context, venue string) error {
	m.stopAdapter(venue)
	if err := m.store.SetBrokerAccountStatus(ctx, venue, "stopped"); err != nil {
		return fmt.Errorf("disable broker: %w", err)
	}
	log.Printf("[broker] disabled %s", venue)
	return nil
}

// EnableBroker 启用账号：读取凭据重新启动适配器，status=connected（失败回写 failed）。
func (m *BrokerManager) EnableBroker(ctx context.Context, venue string) error {
	a, err := m.store.GetBrokerAccount(ctx, venue)
	if err != nil {
		return fmt.Errorf("get broker %s: %w", venue, err)
	}
	if err := m.startAdapter(ctx, a); err != nil {
		_ = m.store.SetBrokerAccountStatus(ctx, venue, "failed")
		return err
	}
	if err := m.store.SetBrokerAccountStatus(ctx, venue, "connected"); err != nil {
		return fmt.Errorf("enable broker: %w", err)
	}
	log.Printf("[broker] enabled %s", venue)
	return nil
}
