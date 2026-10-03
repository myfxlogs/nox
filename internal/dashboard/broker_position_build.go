// broker_position_build.go D-124：视图构建 + 保证金分摊（从 broker_position_monitor.go 拆分，≤300 行）。
// D-571 包A：build 改为逐仓 protoFor 复用——全量视图（Snapshot/回放）与逐仓事件（flush）同一构建点，
// 防两处构建逻辑分叉（fix-must-enumerate-all-writer-paths）。
package dashboard

import (
	"context"
	"log"
	"math"
	"time"

	"arb/internal/bus"
	"arb/internal/venue"

	v1 "arb/internal/gen/dashboardv1"

	"arb/internal/keydomain"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// protoFor 单仓显示态构建（D-571 包A：逐仓事件 flush 唯一组装点）。
// canonMap/csMap 取自 venueState（reconcile 时构建缓存，帧 patch 沿用）。
func (m *BrokerPositionMonitor) protoFor(ctx context.Context, vk string, vs venueState, o venue.Order, now time.Time) *v1.BrokerPosition {
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
			// D-009 S2：帧携双价优先；缺 bid/ask（旧帧/测试）回退中价同值填充。
			bid, ask := o.PriceBid, o.PriceAsk
			if bid <= 0 {
				bid = o.CurrentPrice
			}
			if ask <= 0 {
				ask = o.CurrentPrice
			}
			price = bus.Quote{Bid: bid, Ask: ask, Time: o.PriceAt}
			hasPrice = true
		}
	} else if q, ok := m.qbus.LatestLeg(canonical, o.Symbol, o.VenueKey); ok {
		// D-481：腿级查找走 LatestLeg——CEX 以原生 symbol 归一键优先（canonical 派生
		// 已与采集键同形时天然命中；canonical 为空/错位场景原生键兜底），MT4 canonical 直查。
		price = q
		hasPrice = true
	}
	// D-008 S1：保证金列=venue 原生 InitialMargin 直显（CEX perp iw；全仓 iw=0→空=venue 语义）。
	// D-031 ②：MT5=仓位级原生 RequiredMargin RPC（symbol, lots, DealBuy/DealSell, openPrice）
	// 带缓存（持仓集签名/init_margin_rate 变化失效——禁每帧 RPC）；失败=诚实空态。
	margin := o.InitialMargin
	if keydomain.IsType(o.VenueKey, "mt5") {
		if mv, ok := m.cachedRequiredMargin(ctx, vk, o.VenueKey, o.Symbol, canonical, o.Side, o.Ticket, o.Volume, o.OpenPrice); ok {
			margin = mv
		}
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

// prepareVenueView reconcile 时一次性构建 venue 显示缓存（symMap/csMap）。
// 帧 patch 沿用缓存（canonical 低频变化）。
func (m *BrokerPositionMonitor) prepareVenueView(ctx context.Context, vk string, orders []venue.Order, online bool) venueState {
	vs := venueState{online: online, orders: make(map[string]venue.Order, len(orders))}
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
	return vs
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
