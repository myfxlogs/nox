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
		})
	}
	return out
}
