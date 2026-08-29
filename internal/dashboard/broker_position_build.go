// broker_position_build.go D-124：build 视图构建 + 保证金分摊（从 broker_position_monitor.go 拆分，≤300 行）。
package dashboard

import (
	"context"
	"maps"
	"sort"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/reconcile"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// build 按 venueKey 排序（确定性）构建全量视图：canonical 解析 + 现价（后端算，D-043）+ 保证金分摊（D-124 方案 C）。
// 离线 venue 的持仓仍显示（最近快照），venue_online=false 诚实标注。
func (m *BrokerPositionMonitor) build(ctx context.Context) []*v1.BrokerPosition {
	m.mu.RLock()
	keys := make([]string, 0, len(m.venues))
	for k := range m.venues {
		keys = append(keys, k)
	}
	venues := maps.Clone(m.venues)
	m.mu.RUnlock()
	sort.Strings(keys)

	// D-124：账户保证金 map（venueKey → margin），acctMon nil 或无快照 → 空 map（margin=0）。
	accountMargins := map[string]float64{}
	if m.acctMon != nil {
		for _, ai := range m.acctMon.Snapshot() {
			accountMargins[ai.VenueKey] = ai.Margin
		}
	}

	out := make([]*v1.BrokerPosition, 0, 16)
	canonMaps := map[string]map[string]string{} // venue → symbol→canonical（懒加载，一次轮询内复用）
	csMaps := map[string]map[string]float64{}   // venue → symbol→contractSize（D-124）
	now := time.Now()
	for _, vk := range keys {
		vs := venues[vk]
		venue := ""
		if len(vs.orders) > 0 {
			venue = vs.orders[0].Venue
		}
		// 懒加载 symMap + csMap（一次查询取 canonical + contractSize）
		if venue != "" {
			if _, ok := canonMaps[venue]; !ok {
				cm, cs := m.bm.BrokerSymbolMapWithCS(ctx, venue)
				canonMaps[venue] = cm
				csMaps[venue] = cs
			}
		}
		// D-124：per-venue 保证金分摊（方案 C）；D-146 S5 微件③：CEX perp 有 VenueOrder.InitialMargin 时优先用之。
		marginByTicket := allocateMargins(vs.orders, accountMargins[vk], csMaps[venue])
		for _, o := range vs.orders {
			canonical := ""
			if cm, ok := canonMaps[o.Venue]; ok {
				canonical = cm[o.Symbol]
			}
			// CEX perp/spot 无 symbol_mappings 记录时，从符号派生 canonical（与采集器同构）：
			// binance TRXUSDT → TRXUSDT；okx TRX-USDT-SWAP → TRXUSDT（去 -SWAP + 替换 -）。
			// 无映射则 qbus.Latest 查不到 → 现价显示「—」；派生后可查到实时报价。
			if canonical == "" && isCEXVenueKey(o.VenueKey) {
				canonical = cexCanonical(o.Symbol)
			}
			var price bus.Quote
			hasPrice := false
			if canonical != "" {
				if q, ok := m.qbus.Latest(canonical, o.VenueKey); ok {
					price = q
					hasPrice = true
				}
			}
			// D-146 S5 微件③：CEX perp per-position margin 优先（venue 权威，替换 0=未知语义）；
			// MT4/MT5 走 D-124 账户分摊（VenueOrder.InitialMargin=0）。
			margin := marginByTicket[o.Ticket]
			if o.InitialMargin > 0 {
				margin = o.InitialMargin
			}
			out = append(out, brokerPositionToProto(o, canonical, price, hasPrice, vs.online, m.freshTTL, now, margin))
		}
	}
	return out
}

// allocateMargins D-124 方案 C：按名义价值比例分摊账户总保证金到各笔持仓。
// notional = volume × contractSize × openPrice。守恒优先：任一笔 contractSize 未知（csMap 无值或 0）
// → 该 venue 全部笔 margin=0（防部分分摊破坏守恒 Σ margin_i = M）。
// accountMargin<=0（CEX / 无账户数据）→ 全 0。单笔 → 100%。
func allocateMargins(orders []reconcile.VenueOrder, accountMargin float64, csMap map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(orders))
	if accountMargin <= 0 || len(orders) == 0 {
		return out
	}
	// 守恒检查：所有笔 contractSize 必须已知
	notionalByTicket := make(map[string]float64, len(orders))
	var total float64
	for _, o := range orders {
		cs := csMap[o.Symbol]
		if cs <= 0 {
			return out // 任一未知 → 全 0（守恒优先）
		}
		n := o.Volume * cs * o.OpenPrice
		if n <= 0 {
			return out // 量/价异常 → 全 0
		}
		notionalByTicket[o.Ticket] = n
		total += n
	}
	if total <= 0 {
		return out
	}
	for ticket, n := range notionalByTicket {
		out[ticket] = accountMargin * n / total
	}
	return out
}

// isCEXVenueKey 判断 venueKey 是否为 CEX（binance/okx/gate 的 spot/perp）。
// CEX 无 symbol_mappings 记录，需从符号派生 canonical。
func isCEXVenueKey(vk string) bool {
	return strings.HasPrefix(vk, "binance:") || strings.HasPrefix(vk, "okx:") || strings.HasPrefix(vk, "gate:")
}

// cexCanonical 从 CEX 交易所符号派生 canonical（与采集器同构）：
// binance TRXUSDT → TRXUSDT；okx TRX-USDT-SWAP → TRXUSDT（去 -SWAP + 替换 -）。
func cexCanonical(symbol string) string {
	s := strings.TrimSuffix(symbol, "-SWAP")
	return strings.ReplaceAll(s, "-", "")
}
