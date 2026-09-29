// broker_position_build.go D-124：视图构建 + 保证金分摊（从 broker_position_monitor.go 拆分，≤300 行）。
// D-571 包A：build 改为逐仓 protoFor 复用——全量视图（Snapshot/回放）与逐仓事件（flush）同一构建点，
// 防两处构建逻辑分叉（fix-must-enumerate-all-writer-paths）。
package dashboard

import (
	"context"
	"log"
	"maps"
	"math"
	"sort"
	"time"

	"arb/internal/bus"
	"arb/internal/reconcile"

	v1 "arb/internal/gen/dashboardv1"

	"arb/internal/keydomain"
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

	now := time.Now()
	out := make([]*v1.BrokerPosition, 0, 16)
	for _, vk := range keys {
		vs := venues[vk]
		for _, o := range vs.orders {
			out = append(out, m.protoFor(ctx, vk, vs, o, now))
		}
	}
	return out
}

// protoFor 单仓显示态构建（D-571 包A：全量 build 与逐仓事件 flush 共用，唯一组装点）。
// canonical/现价/swapChargeAt/periodicPct 规则与原 build 内联路径逐字一致（逻辑原样搬迁）；
// canonMap/csMap/marginByTicket 取自 venueState（reconcile 时构建缓存，帧 patch 沿用）。
func (m *BrokerPositionMonitor) protoFor(ctx context.Context, vk string, vs venueState, o reconcile.VenueOrder, now time.Time) *v1.BrokerPosition {
	venue := o.Venue
	canonical := ""
	if vs.canon != nil {
		canonical = vs.canon[o.Symbol]
	}
	// CEX perp/spot 无 symbol_mappings 记录时，从符号派生 canonical（与采集器同构）：
	// binance TRXUSDT → TRXUSDT；okx TRX-USDT-SWAP → TRXUSDT（去 -SWAP + 替换 -）；gate CL_USDT → CLUSDT。
	// 无映射则 qbus.Latest 查不到 → 现价显示「—」；派生后可查到实时报价。
	if canonical == "" && isCEXVenueKey(o.VenueKey) {
		canonical = bus.CexSymbolKey(o.Symbol)
	}
	var price bus.Quote
	hasPrice := false
	// D-571C2 S2：MT5 现价只认帧携（OnOrderProfit 帧同次发布取价，一帧一价一钟——
	// 与 PnL 同帧，业主法条④「流内字段禁第二源拼接」）；PriceAt=0=无帧源
	//（REST 回读行/报价流断）→ 诚实空态 Fresh=false，不回落 LatestLeg。
	if keydomain.IsType(o.VenueKey, "mt5") {
		if !o.PriceAt.IsZero() && o.CurrentPrice > 0 {
			price = bus.Quote{Bid: o.CurrentPrice, Ask: o.CurrentPrice, Time: o.PriceAt}
			hasPrice = true
		}
	} else if q, ok := m.qbus.LatestLeg(canonical, o.Symbol, o.VenueKey); ok {
		// D-481：腿级查找走 LatestLeg——CEX 以原生 symbol 归一键优先（canonical 派生
		// 已与采集键同形时天然命中；canonical 为空/错位场景原生键兜底），MT4 canonical 直查。
		price = q
		hasPrice = true
	}
	// D-146 S5 微件③：CEX perp per-position margin 优先（venue 权威，替换 0=未知语义）；
	// MT4/MT5 走 D-124 账户分摊（VenueOrder.InitialMargin=0）。
	margin := vs.marginByTicket[o.Ticket]
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
	return brokerPositionToProto(o, canonical, price, hasPrice, vs.online, m.freshTTL, now, margin, swapChargeAt, periodicPct)
}

// prepareVenueView reconcile 时一次性构建 venue 显示缓存（symMap/csMap/margin 分摊）。
// 帧 patch 沿用缓存（canonical/保证金低频变化；保证金分摊在 patch 时按缓存重算）。
func (m *BrokerPositionMonitor) prepareVenueView(ctx context.Context, vk string, orders []reconcile.VenueOrder, online bool) venueState {
	vs := venueState{online: online, orders: make(map[string]reconcile.VenueOrder, len(orders))}
	venue := ""
	for _, o := range orders {
		vs.orders[o.Ticket] = o
		if venue == "" {
			venue = o.Venue
		}
	}
	if venue != "" {
		// D-575 F-2：同类漏闸——flush 链上 DB 调用包 per-call 界。
		sctx, cancel := monitorCallCtx(ctx)
		vs.canon, vs.cs = m.bm.BrokerSymbolMapWithCS(sctx, venue)
		cancel()
	}
	var acctMargin float64
	if m.acctMon != nil {
		for _, ai := range m.acctMon.Snapshot() {
			if ai.VenueKey == vk {
				acctMargin = ai.Margin
				break
			}
		}
	}
	vs.acctMargin = acctMargin
	vs.marginByTicket = allocateMargins(orders, acctMargin, vs.cs)
	return vs
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
	switch keydomain.BareOf(vk) {
	case "binance", "okx", "gate":
		return true
	}
	return false
}
