package exec

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"google.golang.org/grpc/metadata"
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
	ctx = metadata.AppendToOutgoingContext(ctx, "id", e.token)
	op := mt5pb.OrderType_OrderType_Buy
	if o.Side == "SELL" {
		op = mt5pb.OrderType_OrderType_Sell
	}
	// MT5 Comment 是 oneof 指针字段，取本地副本地址传入。
	// 与 MT4 统一携带 OrderComment 标记（D-032：对账判别 ARB 源订单）。
	comment := OrderComment
	req := &mt5pb.OrderSendRequest{
		Id:        e.token,
		Symbol:    o.Symbol,
		Operation: op,
		Volume:    o.Qty,
		Comment:   &comment,
	}
	// D-146 S1：灾难 SL 原子写入（Stoploss 是 oneof *float64，非零才挂）。
	if o.StopLoss > 0 {
		sl := o.StopLoss
		req.Stoploss = &sl
	}
	resp, err := e.trading.OrderSend(ctx, req)
	if err != nil {
		return Fill{}, fmt.Errorf("mt5 ordersend: %w", err)
	}
	if err := resp.GetError(); err != nil {
		// D-077 修复⑩ G3：错误码写入 message 供 errclass.Classify 解析（[code=XXX] 前缀）。
		return Fill{}, fmt.Errorf("mt5 ordersend: [code=%s] %s", err.GetCode().String(), err.GetMessage())
	}
	ord := resp.GetResult()
	if ord == nil {
		return Fill{}, fmt.Errorf("mt5 ordersend: nil result")
	}
	// D-179 S4：overfill 兜底告警——MT 服务端规整 qty 可能大于请求（posID=470 实证 0.0109→0.02）。
	// 单已成交不可撤，仅告警留痕（digest 可捕捉）。
	if filled := ord.GetLots(); filled > o.Qty*1.001 {
		log.Printf("[exec] ERROR overfill: requested %.4f filled %.4f symbol=%s venue=%s",
			o.Qty, filled, o.Symbol, e.venueKey)
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
	ctx = metadata.AppendToOutgoingContext(ctx, "id", e.token)
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
		return Fill{}, fmt.Errorf("mt5 orderclose: [code=%s] %s", err.GetCode().String(), err.GetMessage())
	}
	ord := resp.GetResult()
	if ord == nil {
		return Fill{}, fmt.Errorf("mt5 orderclose: nil result")
	}
	// OrderClose 返回 Order 是平仓后状态：Lots=剩余持仓（全平=0），CloseLots=本次平仓手数。
	// 优先 CloseLots；旧 gateway 不填 CloseLots 时 fallback 到 Lots（兼容）。
	qty := ord.GetCloseLots()
	if qty <= 0 {
		qty = ord.GetLots()
	}
	return Fill{
		OrderID:    ref.OrderID,
		Price:      ord.GetClosePrice(),
		Qty:        qty,
		Swap:       ord.GetSwap(),
		Commission: ord.GetCommission(),
		Ts:         time.Now(),
	}, nil
}
