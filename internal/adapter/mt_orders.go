package adapter

import (
	"strconv"
	"time"

	"arb/internal/keydomain"
	"arb/internal/venue"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
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
			// D-031 ①：venue 原生现价——MT5 Order.ClosePrice 对持仓即当前市价（平仓价字段
			// 在持仓单=现价，venue 权威）。帧路径 publishPositions 随后 lastQuote 有价则覆盖双价，
			// 手动仓 lastQuote 空=保留此单价——面板不再「—」。
			CurrentPrice: ord.GetClosePrice(),
			// D-162 ①：只给真实价盖戳——ClosePrice>0（venue 当刻报告价）才 PriceAt=now；
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
