package exec

import (
	"context"
	"fmt"
	"strconv"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// MT5Executor 复用 MT5Adapter 的 gRPC 连接执行交易（§13.2）。
// 与 MT4 严格隔离——独立 proto 类型、独立类。
type MT5Executor struct {
	venue    string
	trading  mt5pb.TradingClient
	token    string
	venueKey string // "venue:mt5"
}

// NewMT5Executor 创建 MT5 执行器。
func NewMT5Executor(venue, token string, trading mt5pb.TradingClient) *MT5Executor {
	return &MT5Executor{
		venue:    venue,
		trading:  trading,
		token:    token,
		venueKey: venue + ":mt5",
	}
}

func (e *MT5Executor) Key() string { return e.venueKey }

func (e *MT5Executor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	op := mt5pb.OrderType_OrderType_Buy
	if o.Side == "SELL" {
		op = mt5pb.OrderType_OrderType_Sell
	}
	resp, err := e.trading.OrderSend(ctx, &mt5pb.OrderSendRequest{
		Id:        e.token,
		Symbol:    o.Symbol,
		Operation: op,
		Volume:    o.Qty,
	})
	if err != nil {
		return Fill{}, fmt.Errorf("mt5 ordersend: %w", err)
	}
	if err := resp.GetError(); err != nil {
		return Fill{}, fmt.Errorf("mt5 ordersend: %s", err.GetMessage())
	}
	ord := resp.GetResult()
	if ord == nil {
		return Fill{}, fmt.Errorf("mt5 ordersend: nil result")
	}
	return Fill{
		OrderID:    strconv.FormatInt(ord.GetTicket(), 10),
		Price:      ord.GetOpenPrice(),
		Qty:        ord.GetLots(),
		Swap:       0,
		Commission: ord.GetCommission(),
		Ts:         time.Now(),
	}, nil
}

func (e *MT5Executor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	ticket, err := strconv.ParseInt(ref.OrderID, 10, 64)
	if err != nil {
		return Fill{}, fmt.Errorf("mt5 close: bad ticket %q: %w", ref.OrderID, err)
	}
	resp, err := e.trading.OrderClose(ctx, &mt5pb.OrderCloseRequest{
		Id:     e.token,
		Ticket: ticket,
	})
	if err != nil {
		return Fill{}, fmt.Errorf("mt5 orderclose: %w", err)
	}
	if err := resp.GetError(); err != nil {
		return Fill{}, fmt.Errorf("mt5 orderclose: %s", err.GetMessage())
	}
	ord := resp.GetResult()
	if ord == nil {
		return Fill{}, fmt.Errorf("mt5 orderclose: nil result")
	}
	return Fill{
		OrderID:    ref.OrderID,
		Price:      ord.GetClosePrice(),
		Qty:        ord.GetLots(),
		Swap:       ord.GetSwap(),
		Commission: ord.GetCommission(),
		Ts:         time.Now(),
	}, nil
}
