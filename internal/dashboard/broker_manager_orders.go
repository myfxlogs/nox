package dashboard

import (
	"context"
	"fmt"
	"log"

	"arb/internal/exec"
	"arb/internal/reconcile"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// attach 记录 running 并刷新 querier 集合；随后 kick swap 轮询（D-060 F2：attach 即补一轮，
// 覆盖启动竞态——ICMarkets 首轮晚 attach 导致整小时零 swap 数据的实锤形态）。
func (m *BrokerManager) attach(venue string, r *running) {
	m.mu.Lock()
	m.running[venue] = r
	m.mu.Unlock()
	m.syncQueriers()
	m.kickSwap()
}

// kickSwap 触发 swap 补轮（nil 安全：未接线直接返回）。
func (m *BrokerManager) kickSwap() {
	if m.SwapKick != nil {
		m.SwapKick()
	}
}

// queriers 汇总 static + running + cex querier（D-121：CEX perp 持仓查询器并入）。
func (m *BrokerManager) queriers() []reconcile.VenueQuerier {
	m.mu.Lock()
	defer m.mu.Unlock()
	qs := make([]reconcile.VenueQuerier, 0, len(m.static)+len(m.running)+len(m.cexQs))
	for _, s := range m.static {
		qs = append(qs, s.q)
	}
	for _, r := range m.running {
		qs = append(qs, r.querier)
	}
	qs = append(qs, m.cexQs...)
	return qs
}

// SetCEXQueriers D-121：替换 CEX perp 持仓查询器集合（CexPositionManager 每轮刷新调用）。
// 触发 syncQueriers → reconcile + BrokerPositionMonitor 刷新。
func (m *BrokerManager) SetCEXQueriers(qs []reconcile.VenueQuerier) {
	m.mu.Lock()
	m.cexQs = qs
	m.mu.Unlock()
	m.syncQueriers()
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

// VenueOrderResult 逐 venue 查询结果（D-052）：venue 身份 + 持仓 + 失败详情，
// 供 BrokerPositionMonitor 跟踪在线状态（0 持仓成功也知 venue 在线）。
type VenueOrderResult struct {
	VenueKey string                 // "{venue}:{type}"
	Orders   []reconcile.VenueOrder // 查询失败时为 nil
	Err      error                  // nil = 在线
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

// venueOrderResults 逐 venue 查询实际持仓（含失败详情，替代 VenueOrders 的吞错版）。D-052。
func (m *BrokerManager) venueOrderResults(ctx context.Context) []VenueOrderResult {
	var out []VenueOrderResult
	for _, q := range m.queriers() {
		if q == nil {
			continue
		}
		vk := q.VenueKey()
		orders, err := q.QueryOpenedOrders(ctx)
		if err != nil {
			log.Printf("[broker] query venue orders %s: %v", vk, err)
		}
		out = append(out, VenueOrderResult{VenueKey: vk, Orders: orders, Err: err})
	}
	return out
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
