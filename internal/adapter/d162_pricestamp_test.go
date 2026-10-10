package adapter

// D-162 ① 锚：MT5OrdersToOrders 只给真实价盖戳——ClosePrice>0 → PriceAt=now
// （venue 当刻报告价）；=0（休市/网关未报价）→ PriceAt 零值，REST 保留闸
// （broker_position_events D-571C2 S2）才能沿用旧帧携价跨休市存活。

import (
	"testing"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

func TestD162_PriceStamp_OnlyRealPrice(t *testing.T) {
	orders := MT5OrdersToOrders("3001157", []*mt5pb.Order{
		{Ticket: 1, Symbol: "XAUUSDm", OrderType: mt5pb.OrderType_OrderType_Buy, Lots: 0.01, OpenPrice: 4180, ClosePrice: 4195.0},
		{Ticket: 2, Symbol: "EURUSDm", OrderType: mt5pb.OrderType_OrderType_Buy, Lots: 0.1, OpenPrice: 1.08, ClosePrice: 0},
	})
	if len(orders) != 2 {
		t.Fatalf("want 2 orders, got %d", len(orders))
	}
	if orders[0].PriceAt.IsZero() {
		t.Fatal("ClosePrice>0 want PriceAt=now")
	}
	if !orders[1].PriceAt.IsZero() || orders[1].CurrentPrice != 0 {
		t.Fatalf("ClosePrice=0 want PriceAt zero+cp=0, got %v/%v", orders[1].PriceAt, orders[1].CurrentPrice)
	}
}
