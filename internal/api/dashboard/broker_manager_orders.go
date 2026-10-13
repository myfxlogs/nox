package dashboard

import (
	"context"
	"fmt"
	"log"

	"nox/internal/data/fact"
	"nox/internal/data/venue"
	"nox/internal/ledger/reconcile"
	"nox/internal/trading/exec"

	v1 "nox/internal/gen/dashboardv1"
)

// attach 记录 running 并刷新 querier 集合；随后 kick swap 轮询（D-060 F2：attach 即补一轮，
// 覆盖启动竞态——ICMarkets 首轮晚 attach 导致整小时零 swap 数据的实锤形态）。
func (m *BrokerManager) attach(venue string, r *running) {
	m.mu.Lock()
	m.running[venue] = r
	m.mu.Unlock()
	m.syncQueriers()
	m.syncHistQueriers()
	m.kickSwap()
	m.kickSessions() // D-029 R2：attach 后立即补一轮周表（同 SwapKick 盲窗修复）
}

// kickSwap 触发 swap 补轮（nil 安全：未接线直接返回）。
func (m *BrokerManager) kickSwap() {
	if m.SwapKick != nil {
		m.SwapKick()
	}
}

// kickSessions D-029 R2 触发周表补轮（nil 安全：未接线直接返回）。
func (m *BrokerManager) kickSessions() {
	if m.SessionsKick != nil {
		m.SessionsKick()
	}
}

// queriers 汇总 static + running + cex querier（D-121：CEX perp 持仓查询器并入）。
func (m *BrokerManager) queriers() []venue.Querier {
	m.mu.Lock()
	defer m.mu.Unlock()
	qs := make([]venue.Querier, 0, len(m.static)+len(m.running)+len(m.cexQs))
	for _, s := range m.static {
		if s.q != nil {
			qs = append(qs, s.q)
		}
	}
	for _, r := range m.running {
		if r.querier != nil {
			qs = append(qs, r.querier)
		}
	}
	qs = append(qs, m.cexQs...)
	return qs
}

// AddStaticQuerier 登记环境变量适配器的 querier。
func (m *BrokerManager) AddStaticQuerier(venue string, q venue.Querier) {
	m.addStatic(venue, q, nil, nil)
}

// AddStaticSwapSource 为环境变量适配器登记 swap 采集（§3.6，不留静默缺口）。
func (m *BrokerManager) AddStaticSwapSource(venue string, swap func(ctx context.Context) ([]fact.Fact, error)) {
	m.addStatic(venue, nil, swap, nil)
}

// AddStaticSessionsSource D-029 ①：登记 MT5 周交易会话表采集（每日刷新面）。
func (m *BrokerManager) AddStaticSessionsSource(venue string, sessions func(ctx context.Context) ([]fact.Fact, error)) {
	m.addStatic(venue, nil, nil, sessions)
}

func (m *BrokerManager) addStatic(venue string, q venue.Querier, swap func(ctx context.Context) ([]fact.Fact, error),
	sessions func(ctx context.Context) ([]fact.Fact, error)) {
	m.mu.Lock()
	m.static = append(m.static, staticQuerier{venue: venue, q: q, swap: swap, sessions: sessions})
	m.mu.Unlock()
	m.syncQueriers()
	m.syncHistQueriers()
}

// AddStaticHistoryQuerier 登记环境变量适配器的历史查证器（D-416：与动态 DB 账户同聚合）。
func (m *BrokerManager) AddStaticHistoryQuerier(venue string, hq reconcile.OrderHistoryQuerier) {
	m.mu.Lock()
	m.static = append(m.static, staticQuerier{venue: venue, hq: hq})
	m.mu.Unlock()
	m.syncHistQueriers()
}

// SetCEXQueriers D-121：替换 CEX perp 持仓查询器集合（CexPositionManager 每轮刷新调用）。
// 触发 syncQueriers → reconcile + BrokerPositionMonitor 刷新。
func (m *BrokerManager) SetCEXQueriers(qs []venue.Querier) {
	m.mu.Lock()
	m.cexQs = qs
	m.mu.Unlock()
	m.syncQueriers()
	m.syncHistQueriers()
}

// KickPositions D-009 S1：passthrough 到 bpMon.Kick（新注册 querier 叫醒回读；
// pendingKick 去重+cap1 信号，bpMon.Run 未起/在跑都安全）。nil-guard=未接线静默。
func (m *BrokerManager) KickPositions(venueKey string) {
	if m.bpm == nil {
		return
	}
	m.bpm.Kick(venueKey)
}

// syncQueriers 汇总 querier 并触发回调（reconciler 刷新）。
func (m *BrokerManager) syncQueriers() {
	qs := m.queriers()
	m.mu.Lock()
	onQ := m.onQ
	m.mu.Unlock()
	if onQ != nil {
		onQ(qs)
	}
}

// histQueriers 汇总 static + running 的历史查证器（D-416：镜像 queriers() 组合）。
func (m *BrokerManager) histQueriers() []reconcile.OrderHistoryQuerier {
	m.mu.Lock()
	defer m.mu.Unlock()
	qs := make([]reconcile.OrderHistoryQuerier, 0, len(m.static)+len(m.running))
	for _, s := range m.static {
		if s.hq != nil {
			qs = append(qs, s.hq)
		}
	}
	for _, r := range m.running {
		if r.histQuerier != nil {
			qs = append(qs, r.histQuerier)
		}
	}
	return qs
}

// syncHistQueriers 汇总历史查证器并触发回调（D-416：reconciler.SetOrderHistoryQueriers 刷新）。
// 与 syncQueriers 同点位调用——attach/detach/addStatic 任一变动即镜像。
func (m *BrokerManager) syncHistQueriers() {
	qs := m.histQueriers()
	m.mu.Lock()
	onHQ := m.onHQ
	m.mu.Unlock()
	if onHQ != nil {
		onHQ(qs)
	}
}

// ServerTZQueriers D-337 S2：汇总实现了 ServerTZQuerier 的 querier（venueKey→querier）。
// 每轮重取跟随 attach/detach；仅 MT5 实现（MT4/CEX 不在 map=swap 倒计时诚实空态）。
func (m *BrokerManager) ServerTZQueriers() map[string]reconcile.ServerTZQuerier {
	out := make(map[string]reconcile.ServerTZQuerier)
	for _, q := range m.queriers() {
		if zq, ok := q.(reconcile.ServerTZQuerier); ok && zq != nil {
			out[q.VenueKey()] = zq
		}
	}
	return out
}

// VenueOrderResult 逐 venue 查询结果（D-052）：venue 身份 + 持仓 + 失败详情，
// 供 BrokerPositionMonitor 跟踪在线状态（0 持仓成功也知 venue 在线）。
type VenueOrderResult struct {
	VenueKey string        // "{venue}:{type}"
	Orders   []venue.Order // 查询失败时为 nil
	Err      error         // nil = 在线
}

// AccountQueriers 汇总 static + running 中实现了 AccountQuerier 的 querier（D-053）。
// AccountMonitor 每轮重取 → venue 启停（AddBroker/RemoveBroker）自动跟随。
func (m *BrokerManager) AccountQueriers() []reconcile.AccountQuerier {
	var out []reconcile.AccountQuerier
	for _, q := range m.queriers() {
		if aq, ok := q.(reconcile.AccountQuerier); ok && aq != nil {
			out = append(out, aq)
		}
	}
	return out
}

// venueOrderResults 逐 venue 查询实际持仓（含失败详情，替代 venue.Orders 的吞错版）。D-052。
func (m *BrokerManager) venueOrderResults(ctx context.Context) []VenueOrderResult {
	var out []VenueOrderResult
	for _, q := range m.queriers() {
		if q == nil {
			continue
		}
		vk := q.VenueKey()
		// D-575：querier 级 per-call 10s 界——venue RPC 挂死不再冻结 Run 循环（循环内显式 cancel 禁 defer 累积）。
		qctx, cancel := monitorCallCtx(ctx)
		orders, err := q.QueryOpenedOrders(qctx)
		cancel()
		if err != nil {
			log.Printf("[broker] query venue orders %s: %v", vk, err)
		}
		out = append(out, VenueOrderResult{VenueKey: vk, Orders: orders, Err: err})
	}
	return out
}

// venueOrders D-571 包A：单 venue 权威查询（逐仓事件化的权威重读点——未知票号/
// kick/流重连按 venue 回读，不再全量轮询）。querier 缺席 → Err=not found（诚实失败）。
func (m *BrokerManager) venueOrders(ctx context.Context, venueKey string) VenueOrderResult {
	for _, q := range m.queriers() {
		if q == nil || q.VenueKey() != venueKey {
			continue
		}
		// D-575：同上 per-call 10s 界。
		qctx, cancel := monitorCallCtx(ctx)
		orders, err := q.QueryOpenedOrders(qctx)
		cancel()
		if err != nil {
			log.Printf("[broker] query venue orders %s: %v", venueKey, err)
		}
		return VenueOrderResult{VenueKey: venueKey, Orders: orders, Err: err}
	}
	return VenueOrderResult{VenueKey: venueKey, Err: fmt.Errorf("querier not found for %s", venueKey)}
}

// PlaceOrder 经注册执行器下单（D-047 探针仓开仓专用薄封装——venue_key→Executor
// 注册表解引用单点；CEX/MT 各 executor 内部形态差异由其自身吸收）。
// [exec-entry:shared-executor] D-262 M4.3③：本件=传输层非入口——开仓分类责在
// 各调用方（probe=committee-exempt、spot_carry_demo=demo、pipeline=gated-body）。
func (m *BrokerManager) PlaceOrder(ctx context.Context, o exec.Order) (exec.Fill, error) {
	e, ok := m.reg.Get(o.Venue)
	if !ok {
		return exec.Fill{}, fmt.Errorf("executor not found for %s", o.Venue)
	}
	return e.PlaceOrder(ctx, o)
}

// CloseOrder 经注册执行器平仓一条 venue 持仓。D-051 WP3。
// MT venue：按 ticket 调 CloseOrder（OrderClose）。
// CEX venue：CEX 无 CloseOrder 概念 → 从 bpm 快照查匹配 venueKey+ticket 的持仓
// （side/volume/symbol）→ PlaceOrder 反向减仓（D-121：聚合仓位 ticket=Symbol）。
func (m *BrokerManager) CloseOrder(ctx context.Context, venueKey, ticket string) (exec.Fill, error) {
	e, ok := m.reg.Get(venueKey)
	if !ok {
		return exec.Fill{}, fmt.Errorf("executor not found for %s", venueKey)
	}
	if isMTVenue(venueKey) {
		return e.CloseOrder(ctx, exec.OrderRef{OrderID: ticket, Venue: venueKey})
	}
	// CEX venue：从 bpm 快照查持仓 side/volume/symbol → PlaceOrder 反向
	bps := m.bpm.Snapshot()
	var bp *v1.BrokerPosition
	for _, b := range bps {
		if b.VenueKey == venueKey && b.Ticket == ticket {
			bp = b
			break
		}
	}
	if bp == nil {
		return exec.Fill{}, fmt.Errorf("cex close %s: position not found in snapshot (ticket=%s)", venueKey, ticket)
	}
	if bp.Volume <= 0 {
		return exec.Fill{}, fmt.Errorf("cex close %s: zero volume (ticket=%s)", venueKey, ticket)
	}
	closeSide := "SELL"
	if bp.Side == "SELL" {
		closeSide = "BUY"
	}
	// PositionSide 设原持仓侧 → OKX 减对应仓位 / gate 触发 reduce_only / Binance one-way 触发 reduceOnly。
	// 与 pipeline_legs.go ClosePosition CEX 路径一致（D-059/D-108/D-120/D-121）。
	var positionSide string
	switch bp.Side {
	case "BUY":
		positionSide = "long"
	case "SELL":
		positionSide = "short"
	}
	return e.PlaceOrder(ctx, exec.Order{
		Symbol:       bp.Symbol,
		Side:         closeSide,
		Qty:          bp.Volume,
		Venue:        venueKey,
		PositionSide: positionSide,
	})
}
