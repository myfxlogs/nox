// Broker 管理器：运行时 MT4/MT5 连接的生命周期所有者（docs/broker管理器实现方案.md §3/§5）。
// 统一管理 DB 账号的适配器启停、报价订阅、执行器/对账注册，支持前端动态添加/删除。
package dashboard

import (
	"arb/internal/keydomain"
	"context"
	"fmt"
	"sync"
	"time"

	"arb/internal/adapter"
	"arb/internal/alertcool"
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
	histQuerier  reconcile.OrderHistoryQuerier                    // D-416：对账历史查证器（attach/detach 镜像注册）
	symbols      func(ctx context.Context) ([]string, error)      // 拉取服务器符号表
	symbolGroups func(ctx context.Context) ([]SymbolGroup, error) // 符号表 + 分组（仅服务 BrokerSymbols 展示，按需拉一次）
	// volumeSpec 单符号最小手数（MT5 SymbolParams SymGroup.MinLots；MT4/未运行 = nil）。
	// D-061 量规格预检用；known=false = 未知，调用方放行（venue 服务端拒单兜底）。
	volumeSpec func(ctx context.Context, symbol string) (float64, bool, error)
	// volumeStep 单符号 volume step（D-070 qty 精度取整）；MT4/未运行 = nil。
	volumeStep func(ctx context.Context, symbol string) (float64, bool, error)
	// symbolParams 单符号权威合约规格（MT5 SymbolParams；MT4/未运行 = nil）。零信任：
	// 保存映射的合约校验数据只信 broker 自取，不信前端传值（D-042）。
	symbolParams     func(ctx context.Context, symbol string) (float64, bool, error)
	setSymMap        func(map[string]string)                                     // 运行时刷新 brokerSymbol → canonical
	setEmitBlocked   func(map[string]bool)                                       // D-383 A2.3：运行时刷新 canonical 发射抑制集
	setUnitScales    func(map[string]adapter.SymbolScale, map[string]bool)       // D-594：运行时刷新单位标定+敏感集
	resubscribe      func([]string)                                              // 运行时补订阅新符号（保存映射后生效，D-047 缺陷③）
	swap             func(ctx context.Context) ([]fact.Fact, error)              // 轮询 swap facts（§3.6，D-036）
	meta             func(ctx context.Context) ([]fact.Fact, error)              // D-140 S2：MT5 符号元数据 facts（CS 采集）
	orderHistoryScan orderHistoryScan                                            // D-181 S1：拉历史已平仓订单（BackfillSwapHistory RPC 用），nil=未接线
	acctSummary      func(ctx context.Context) (float64, float64, string, error) // D-563 S4b：MT 实时 equity/freeMargin/currency（AccountSummary RPC）；nil=非 MT 或未接线

	// per-venue symbolGroups TTL 缓存（D-045 §3.2.1）：避免矩阵页全量拉取时 N 账号 × SymbolList+SymbolParamsMany
	// 直连 mtapi；缓存失效回退直连，失败不下发脏数据。
	sgMu      sync.Mutex
	sgCache   []SymbolGroup
	sgCacheAt time.Time
}

// MTAccountSummary D-563 S4b：venue_key → MT 实时账户摘要（决策面禁快照）。
// venueKey 带域（"3001157:mt5"）剥域取 venue 段查 running；非 MT/未接线/RPC 败 → error。
func (m *BrokerManager) MTAccountSummary(ctx context.Context, venueKey string) (equity, freeMargin float64, currency string, err error) {
	v := keydomain.Parse(venueKey).Venue
	m.mu.Lock()
	r, ok := m.running[v]
	m.mu.Unlock()
	if !ok || r.acctSummary == nil {
		return 0, 0, "", fmt.Errorf("MT 实时资金不可得：%s 适配器未接线", venueKey)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return r.acctSummary(ctx)
}

// staticQuerier 环境变量适配器的 querier（不被动态管理，boot 时与 DB venue 去重）。
type staticQuerier struct {
	venue string
	q     reconcile.VenueQuerier
	hq    reconcile.OrderHistoryQuerier                  // D-416：env 静态历史查证器
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
	cexQs   []reconcile.VenueQuerier              // D-121：CEX perp 持仓查询器（动态刷新）
	onQ     func([]reconcile.VenueQuerier)        // querier 集合变更回调
	onHQ    func([]reconcile.OrderHistoryQuerier) // D-416：历史查证器集合变更回调
	bpm     *BrokerPositionMonitor                // D-121：CEX 平仓需查 bpm 快照获取 side/volume（注入，nil=未接线）

	commMu     sync.Mutex
	commission map[string]float64 // venue → 账号真实佣金（bps/腿，D-036）；缺省回退 config 默认

	// 冷启动退避（m.mu 保护）：任一 venue 映射符号缺报价 → 重试直到全部流入；
	// 超过 coldStartBound 仍缺 → 降级接受部分（防病态 symbol 永久阻塞整轮轮询）。
	coldStartSince time.Time

	// symbolGroups TTL（D-045 §3.2.1）：由 config.SymbolsTTL 注入。
	symbolGroupsTTL time.Duration

	marginReservePct float64 // D-415 动态 MT5 executor 资金预检安全垫（cfg 注入）

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

	// 晚接 sink 四件（sinkMu 保护；D-581）：目标经 Set* 晚接，startMT5 拷贝的是
	// dispatch* bound-method——调用时 RLock 取最新目标，接线时序不敏感。
	// 原导出字段一次性按值拷贝：setupMonitors 赋值晚于 StartAll → adapter 拷到
	// nil 即永久丢帧（浮盈冻结/流态/余额帧同丢）。
	sinkMu          sync.RWMutex
	accountSink     adapter.AcctFrameSink                // D-445 S0：OnOrderUpdate 余额帧（sink=acctMon.ObserveStreamAccount）
	positionsSink   func(string, []reconcile.VenueOrder) // D-571 包A：OnOrderProfit 逐仓载荷（sink=bpMon.OnMT5Frame）
	streamStateSink func(string, bool)                   // D-571 包A：venue 流状态透出（sink=bpMon/acctMon/posMon 链）
	orderProfitKick func()                               // D-107 P2：margin 事件→acctMon.Kick

	// D-104 P2 U1：网关节点启动级 failover（main 从 config 注入，attach 时透传 adapter.SetGateway）。
	MT5Gateway          string
	MT5GatewayFallbacks []string

	// AlertSink 断流分级告警下沉（D-104 P2 U2，MarginSink 同模式三点注入链）。
	// main 注入 func(level, msg string) { _ = st.InsertAlert(ctx, nil, level, msg) }，nil 安全。
	AlertSink func(level, msg string)

	// primaryAlertCool D-383 A2.3：歧义组告警 (venue,canonical) 键 30min 冷却
	//（独立冷却器，不扰动共享 AlertSink 语义）。
	primaryAlertCool *alertcool.Cooler

	// RuleBackfill D-142 ②：AddBroker 后异步触发规则库补录（免重启）。
	// symbolPlan 自动映射（名字匹配）在 startAdapter 内已完成；BackfillRuleMappings 补 regex 规则命中
	// 但名字不匹配的符号（source='rule'）。wiring/main 注入（同 SwapKick 模式），nil=未接线（测试直接构造）。
	RuleBackfill func(ctx context.Context, venue string)
}

// SetQuerierHook 注册 querier 集合变更回调（main 接 reconciler.SetQueriers）。
func (m *BrokerManager) SetQuerierHook(f func([]reconcile.VenueQuerier)) {
	m.onQ = f
}

// SetHistQuerierHook 注册历史查证器集合变更回调（D-416：main 接 reconciler.SetOrderHistoryQueriers）。
func (m *BrokerManager) SetHistQuerierHook(f func([]reconcile.OrderHistoryQuerier)) {
	m.onHQ = f
}

// SetBPM D-121：注入 BrokerPositionMonitor（CEX 平仓需查快照获取 side/volume）。
// 读点在 CEX 平仓路径（boot 窗口不可达），已审计同型晚写不迁（D-581）。
func (m *BrokerManager) SetBPM(bpm *BrokerPositionMonitor) { m.bpm = bpm }

// OrderHistoryScan D-181 S1：返回指定 venue 的历史订单扫描闭包（nil=未连接/无扫描器）。
// BackfillSwapHistory RPC 用——拉 venue 已平仓订单计算 usdLotDay → swap_settled facts。
func (m *BrokerManager) OrderHistoryScan(venue string) orderHistoryScan {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.running[venue]; ok {
		return r.orderHistoryScan
	}
	return nil
}
