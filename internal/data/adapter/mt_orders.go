package adapter

import (
	"context"
	"strconv"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/keydomain"
	"nox/internal/data/venue"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// MT5OrdersToOrders D-571 包A：mt5pb.Order 集 → venue.Order 单一映射点
// （REST 重读与 OnOrderProfit 帧 patch 两路同源，防映射分叉；fix-must-enumerate-all-writer-paths）。
func MT5OrdersToOrders(v string, orders []*mt5pb.Order) []venue.Order {
	var out []venue.Order
	for _, ord := range orders {
		side := "BUY"
		if ord.GetOrderType() == mt5pb.OrderType_OrderType_Sell {
			side = "SELL"
		}
		var openedAt time.Time
		if ts := ord.GetOpenTime(); ts != nil {
			openedAt = ts.AsTime()
		}
		out = append(out, venue.Order{
			Ticket:     strconv.FormatInt(ord.GetTicket(), 10),
			Symbol:     ord.GetSymbol(),
			Side:       side,
			Volume:     ord.GetLots(),
			OpenPrice:  ord.GetOpenPrice(),
			Venue:      v,
			Comment:    ord.GetComment(),
			VenueKey:   keydomain.Join(v, "mt5"),
			Profit:     ord.GetProfit(),
			Swap:       ord.GetSwap(),
			Commission: ord.GetCommission(),
			OpenedAt:   openedAt,
			Magic:      ord.GetExpertId(),
			StopLoss:   ord.GetStopLoss(), // D-155：MT5 Order.StopLoss 透传
			// ①：venue 原生现价——MT5 Order.ClosePrice 对持仓即当前市价（平仓价字段
			// 在持仓单=现价，venue 权威）。帧路径 publishPositions 随后 lastQuote 有价则覆盖双价，
			// 手动仓 lastQuote 空=保留此单价——面板不再「—」。
			CurrentPrice: ord.GetClosePrice(),
			// ①：只给真实价盖戳——ClosePrice>0（venue 当刻报告价）才 PriceAt=now；
			// =0（休市/网关未报价）留零值，REST 保留闸（broker_position_events）才能
			// 沿用旧帧携价跨休市存活——恒盖 now 会把周五帧携价冲成 (0,now)。
			PriceAt: priceStamp(ord.GetClosePrice()),
		})
	}
	return out
}

// priceStamp D-162：价正 → 盖当前钟（venue 当刻报告价语义）；价≤0 → 零值
// （下游 REST 保留闸据此沿用旧帧携价）。
func priceStamp(price float64) time.Time {
	if price > 0 {
		return time.Now()
	}
	return time.Time{}
}

// LastQuote D-162R：品种报价钟查询口（lastQuote 帧内携价缓存读端）——
// REST 回读路径锚 PriceAt 用（休市帧携价不得盖 now 标 fresh）。
func (a *MT5Adapter) LastQuote(symbol string) (bus.Quote, bool) {
	v, ok := a.lastQuote.Load(symbol)
	if !ok {
		return bus.Quote{}, false
	}
	q, ok := v.(bus.Quote)
	return q, ok
}

// TickAnchor 品种报价钟函数型（D-162R）：原生 symbol → bid/ask/tick 真钟。
// 裸值元组不引 bus（reconcile 方向禁 bus import——D-514 分层）。
// R2 复合钟：lastQuote（内存帧携）优先 → miss 兜底 ticks 表持久末帧（setup 装配注入）。
type TickAnchor func(ctx context.Context, symbol string) (bid, ask float64, ts time.Time, ok bool)

// LastTickAnchor 报价钟锚（lastQuote 缓存读端——复合钟的第一级，单级用时=纯内存锚）。
func (a *MT5Adapter) LastTickAnchor(_ context.Context, symbol string) (float64, float64, time.Time, bool) {
	q, ok := a.LastQuote(symbol)
	return q.Bid, q.Ask, q.Time, ok
}

// ApplyTickQuote D-162R：品种报价钟锚定（帧路径/REST 回读共享单点）——
// Order.Symbol=原生 brokerSym 查 lastTick，命中且双价正 → 覆盖
// CurrentPrice/PriceAt/PriceBid/PriceAsk（**PriceAt=tick 真钟**，禁另钟拼接；
// 休市末 tick 时戳天然判 stale——帧携价不得盖 now 标 fresh）。
// Bid/Ask 缺一非正=置零诚实，零命中=保留映射原值（priceStamp 语义）。
func ApplyTickQuote(ctx context.Context, vos []venue.Order, lastTick TickAnchor) {
	for i := range vos {
		if bid, ask, ts, ok := lastTick(ctx, vos[i].Symbol); ok && bid > 0 && ask > 0 {
			vos[i].CurrentPrice = (bid + ask) / 2
			vos[i].PriceAt = ts
			vos[i].PriceBid = bid // D-009 S2：帧携双价（同 tick 同钟）
			vos[i].PriceAsk = ask
		}
	}
}

// SetPriceAnchor D-162R2：复合报价钟注入（lastQuote 优先→ticks 末帧兜底，setup
// 装配点构造）；nil=纯 lastQuote 单级（测试/装配缺失照原义）。
func (a *MT5Adapter) SetPriceAnchor(fn TickAnchor) { a.priceAnchor = fn }

// ApplyLastQuote D-571C2 S1+D-162R：帧内携价锚定（复合钟——lastQuote miss 时
// ticks 持久末帧兜底，重启+休市场景 PriceAt 仍锚周五 tick 真钟判 stale）。
func (a *MT5Adapter) ApplyLastQuote(ctx context.Context, vos []venue.Order) {
	anchor := a.priceAnchor
	if anchor == nil {
		anchor = a.LastTickAnchor
	}
	ApplyTickQuote(ctx, vos, anchor)
}
