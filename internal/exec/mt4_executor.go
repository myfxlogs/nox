package exec

import (
	"context"
	"fmt"
	"strconv"
	"time"

	mt4pb "arb/internal/adapter/gen/mtapi/mt4"
	"google.golang.org/grpc/metadata"
)

// MT4Executor 复用 MT4Adapter 的 gRPC 连接执行交易（§13.2）。
type MT4Executor struct {
	venue    string
	trading  mt4pb.TradingClient
	token    string
	venueKey string // "venue:mt4"
}

// NewMT4Executor 创建 MT4 执行器。trading 来自已连接的 gRPC conn。
func NewMT4Executor(venue, token string, trading mt4pb.TradingClient) *MT4Executor {
	return &MT4Executor{
		venue:    venue,
		trading:  trading,
		token:    token,
		venueKey: venue + ":mt4",
	}
}

func (e *MT4Executor) Key() string { return e.venueKey }

func (e *MT4Executor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", e.token)
	op := mt4pb.Op_Op_Buy
	if o.Side == "SELL" {
		op = mt4pb.Op_Op_Sell
	}
	resp, err := e.trading.OrderSend(ctx, &mt4pb.OrderSendRequest{
		Id:        e.token,
		Symbol:    o.Symbol,
		Operation: op,
		Volume:    o.Qty,
		Comment:   OrderComment,
		Stoploss:  o.StopLoss, // D-146 S1：灾难 SL 原子写入（0=不挂，MT4 Stoploss 是 float64 非 oneof）
	})
	if err != nil {
		return Fill{}, fmt.Errorf("mt4 ordersend: %w", err)
	}
	if err := resp.GetError(); err != nil {
		// D-077 修复⑩ G3：错误码写入 message 供 errclass.Classify 解析（[code=XXX] 前缀）。
		return Fill{}, fmt.Errorf("mt4 ordersend: [code=%s] %s", err.GetCode().String(), err.GetMessage())
	}
	ord := resp.GetResult()
	if ord == nil {
		return Fill{}, fmt.Errorf("mt4 ordersend: nil result")
	}
	return Fill{
		OrderID:    strconv.FormatInt(int64(ord.GetTicket()), 10),
		Price:      ord.GetOpenPrice(),
		Qty:        ord.GetLots(),
		Swap:       0,
		Commission: ord.GetCommission(),
		Ts:         time.Now(),
	}, nil
}

func (e *MT4Executor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", e.token)
	ticket, err := strconv.ParseInt(ref.OrderID, 10, 32)
	if err != nil {
		return Fill{}, fmt.Errorf("mt4 close: bad ticket %q: %w", ref.OrderID, err)
	}
	resp, err := e.trading.OrderClose(ctx, &mt4pb.OrderCloseRequest{
		Id:     e.token,
		Ticket: int32(ticket),
	})
	if err != nil {
		return Fill{}, fmt.Errorf("mt4 orderclose: %w", err)
	}
	if err := resp.GetError(); err != nil {
		return Fill{}, fmt.Errorf("mt4 orderclose: [code=%s] %s", err.GetCode().String(), err.GetMessage())
	}
	ord := resp.GetResult()
	if ord == nil {
		return Fill{}, fmt.Errorf("mt4 orderclose: nil result")
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
