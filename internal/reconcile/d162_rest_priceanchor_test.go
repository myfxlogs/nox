package reconcile

// D-162R 锚：REST 回读（MT5Querier.QueryOpenedOrders）PriceAt 锚品种报价钟——
// 休市网关回 ClosePrice=末收价>0 时，若盖回读时刻 now 会被误判 fresh；
// SetPriceAnchor 注入 lastTick → PriceAt=tick 真钟（stale 如实）。无锚=照旧 now。

import (
	"context"
	"testing"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"

	"google.golang.org/grpc"
)

type d162FakeMT5Client struct {
	mt5pb.MT5Client
	orders []*mt5pb.Order
}

func (c *d162FakeMT5Client) OpenedOrders(context.Context, *mt5pb.OpenedOrdersRequest, ...grpc.CallOption) (*mt5pb.OpenedOrdersReply, error) {
	return &mt5pb.OpenedOrdersReply{Result: c.orders}, nil
}

func TestD162R_RestPriceAnchor(t *testing.T) {
	tickTs := time.Now().Add(-20 * time.Hour) // 周五末 tick（休市形态）
	cli := &d162FakeMT5Client{orders: []*mt5pb.Order{
		{Ticket: 1, Symbol: "XAUUSDm", OrderType: mt5pb.OrderType_OrderType_Buy, Lots: 0.01, OpenPrice: 4180, ClosePrice: 4195.0},
	}}

	q := NewMT5Querier("v", "tok", cli)
	orders, err := q.QueryOpenedOrders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(orders[0].PriceAt) > time.Minute { // 无锚=照旧盖回读时刻
		t.Fatalf("no-anchor want PriceAt≈now, got %v", orders[0].PriceAt)
	}

	q.SetPriceAnchor(func(_ context.Context, symbol string) (float64, float64, time.Time, bool) {
		return 4194.85, 4195.11, tickTs, true
	})
	orders, err = q.QueryOpenedOrders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !orders[0].PriceAt.Equal(tickTs) {
		t.Fatalf("anchored want PriceAt=tick ts %v, got %v", tickTs, orders[0].PriceAt)
	}
	if orders[0].CurrentPrice != 4194.98 {
		t.Fatalf("anchored want mid 4194.98, got %v", orders[0].CurrentPrice)
	}
}
