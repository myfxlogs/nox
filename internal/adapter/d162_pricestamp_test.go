package adapter

// D-162 ① 锚：MT5OrdersToOrders 只给真实价盖戳——ClosePrice>0 → PriceAt=now
// （venue 当刻报告价）；=0（休市/网关未报价）→ PriceAt 零值，REST 保留闸
// （broker_position_events D-571C2 S2）才能沿用旧帧携价跨休市存活。

import (
	"testing"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/bus"
	"context"
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
	a.ApplyLastQuote(context.Background(), orders)

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

// TestD162R2_CompositeAnchor 复合钟（R2）：SetPriceAnchor 注入（lastQuote 空场景——
// 重启+休市）→ ApplyLastQuote 锚持久末帧真钟判 stale；未注入=纯 lastQuote 单级。
func TestD162R2_CompositeAnchor(t *testing.T) {
	friTs := time.Now().Add(-24 * time.Hour) // 周五末 tick 持久帧
	a := &MT5Adapter{venue: "v"}
	// lastQuote 空（重启后）→ 复合钟兜底 ticks 持久末帧
	a.SetPriceAnchor(func(_ context.Context, sym string) (float64, float64, time.Time, bool) {
		if sym != "XAUUSDm" {
			return 0, 0, time.Time{}, false
		}
		return 4194.85, 4195.11, friTs, true
	})
	orders := MT5OrdersToOrders("v", []*mt5pb.Order{
		{Ticket: 1, Symbol: "XAUUSDm", OrderType: mt5pb.OrderType_OrderType_Buy, Lots: 0.01, OpenPrice: 4180, ClosePrice: 4195.0},
	})
	a.ApplyLastQuote(context.Background(), orders)
	if !orders[0].PriceAt.Equal(friTs) {
		t.Fatalf("composite want PriceAt=fri ts %v, got %v", friTs, orders[0].PriceAt)
	}
	if orders[0].CurrentPrice != 4194.98 {
		t.Fatalf("composite want mid 4194.98, got %v", orders[0].CurrentPrice)
	}
}
