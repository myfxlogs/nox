// broker_position_build.go D-124：build 视图构建 + 保证金分摊（从 broker_position_monitor.go 拆分，≤300 行）。
package dashboard

import (
	"context"
	"log"
	"maps"
	"math"
	"sort"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/reconcile"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"google.golang.org/protobuf/types/known/timestamppb"
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
			// binance TRXUSDT → TRXUSDT；okx TRX-USDT-SWAP → TRXUSDT（去 -SWAP + 替换 -）；gate CL_USDT → CLUSDT。
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
			// D-337 S2 + D-337R1 S2 + D-466 S3/R1：下次计费/结算时刻——MT5=server 零点（tzCache）；
			// CEX=腿级 venue 原生真值回退：funding_next_settle fact（stale 闸=龄>周期×2 失效）
			// → venue 原生 interval 边界 → nil 诚实空态+warn（业主令禁 8h 兜底——猜的值
			// 会看似合理但错）。FundingIntervalCache 仅 binance 腿合法回退（venue 原生
			// 数据）；跨 venue 查用即污染（OKX 腿吃 binance 4h 期实证）。
			var swapChargeAt *timestamppb.Timestamp
			var periodicPct float64
			if isCEXVenueKey(o.VenueKey) && canonical != "" {
				var nextSettle, factTs time.Time
				var ivl time.Duration
				if m.cexNextSettle != nil {
					nextSettle, factTs, ivl = m.cexNextSettle(ctx, venue, canonical)
				}
				periods := 0
				if ivl > 0 {
					periods = int(math.Round(86400 / ivl.Seconds()))
				}
				switch {
				case !nextSettle.IsZero() && nextSettle.After(now) && (ivl <= 0 || now.Sub(factTs) <= 2*ivl):
					swapChargeAt = timestamppb.New(nextSettle)
				case periods > 0:
					// fact 缺席/stale/过期 → venue 原生 interval 边界
					swapChargeAt = timestamppb.New(NextFundingSettlementUTC(now.UTC(), periods))
					log.Printf("[broker] cex settle fact %s|%s unusable (next=%v ts=%v, fallback venue interval %d/day)", venue, canonical, nextSettle, factTs, periods)
				default:
					// R1：binance 腿仅自家 FundingIntervalCache 命中可用（venue 原生合法
					// 回退）；miss 与其余 venue 一律 nil 空态+warn——借它=跨 venue 污染
					// （本票根因），8h 兜底=猜的值禁产（业主令）。
					if venue == "binance" && m.cexIntervals != nil {
						if pp, ok := m.cexIntervals.Lookup(canonical); ok {
							periods = pp
							swapChargeAt = timestamppb.New(NextFundingSettlementUTC(now.UTC(), periods))
						}
					}
					if swapChargeAt == nil {
						log.Printf("[broker] cex settle %s|%s no venue-native fact/interval (nil honest-empty)", venue, canonical)
					}
				}
				// R2：periodicPct 仅 venue 原生 periods（funding_interval fact / binance 自家
				// 缓存命中）可算；非原生来源（默认/污染路径）→0 诚实空态。
				if periods > 0 && m.cexFundingRate != nil {
					if annual, ok2 := m.cexFundingRate(ctx, venue, canonical); ok2 {
						periodicPct = annual / (365 * float64(periods))
					}
				}
			} else if m.tzCache.fetch != nil && !isCEXVenueKey(o.VenueKey) { // D-337R2：零值缓存=诚实空态（构造必选注入）
				if tzLoc := m.tzCache.get(ctx, o.VenueKey); tzLoc != nil {
					swapChargeAt = timestamppb.New(nextSwapChargeAtInLoc(now, tzLoc))
				}
			}
			out = append(out, brokerPositionToProto(o, canonical, price, hasPrice, vs.online, m.freshTTL, now, margin, swapChargeAt, periodicPct))
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
// binance TRXUSDT → TRXUSDT；okx TRX-USDT-SWAP → TRXUSDT（去 -SWAP + 替换 -）；
// gate CL_USDT → CLUSDT（替换 _，镜像 canonicalFromGate=ReplaceAll("_","")，D-311 R6④）。
func cexCanonical(symbol string) string {
	s := strings.TrimSuffix(symbol, "-SWAP")
	s = strings.ReplaceAll(s, "-", "")
	return strings.ReplaceAll(s, "_", "")
}
