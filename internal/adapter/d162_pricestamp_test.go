package adapter

// D-162 ① 锚：MT5OrdersToOrders 只给真实价盖戳——ClosePrice>0 → PriceAt=now
// （venue 当刻报告价）；=0（休市/网关未报价）→ PriceAt 零值，REST 保留闸
// （broker_position_events D-571C2 S2）才能沿用旧帧携价跨休市存活。

import (
	"testing"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/bus"
	"time"
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

// TestD162R_TickClockAnchor 品种报价钟锚定：lastQuote hit → PriceAt=tick 真钟
// （非调用时刻 now）——休市末价判 stale；miss → 保留 priceStamp（无证据才帧时）。
func TestD162R_TickClockAnchor(t *testing.T) {
	tickTs := time.Now().Add(-20 * time.Hour) // 周五末 tick
	a := &MT5Adapter{venue: "v"}
	a.lastQuote.Store("XAUUSDm", bus.Quote{Symbol: "XAUUSDm", Bid: 4194.85, Ask: 4195.11, Time: tickTs})

	orders := MT5OrdersToOrders("v", []*mt5pb.Order{
		{Ticket: 1, Symbol: "XAUUSDm", OrderType: mt5pb.OrderType_OrderType_Buy, Lots: 0.01, OpenPrice: 4180, ClosePrice: 4195.0},
		{Ticket: 2, Symbol: "EURUSDm", OrderType: mt5pb.OrderType_OrderType_Buy, Lots: 0.1, OpenPrice: 1.08, ClosePrice: 1.09},
	})
	a.ApplyLastQuote(orders)

	// hit：价=tick 中价、PriceAt=tick 真钟（≈20h 前 → quoteFresh 判 stale）
	if !orders[0].PriceAt.Equal(tickTs) {
		t.Fatalf("hit want PriceAt=tick ts %v, got %v", tickTs, orders[0].PriceAt)
	}
	if orders[0].CurrentPrice != 4194.98 || orders[0].PriceBid != 4194.85 {
		t.Fatalf("hit want mid/bid 4194.98/4194.85, got %v/%v", orders[0].CurrentPrice, orders[0].PriceBid)
	}
	// miss：保留 priceStamp now（无证据才帧时——营业中回读语义）
	if orders[1].PriceAt.IsZero() || time.Since(orders[1].PriceAt) > time.Minute {
		t.Fatalf("miss want PriceAt≈now, got %v", orders[1].PriceAt)
	}
}
