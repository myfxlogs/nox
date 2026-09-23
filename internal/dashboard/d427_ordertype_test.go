// d427_ordertype_test.go —— D-427 I4：swap 历史扫描 OrderType 白名单（Balance/Credit/未知跳过）。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/adapter/gen/mtapi/mt4"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// T-I4a MT5：Balance=100/Credit=101/未知枚举记录不入结果；Buy/Sell 正常行不受影响。
func TestD427_MT5Scan_OrderTypeWhitelist(t *testing.T) {
	openMs := time.Now().Add(-72 * time.Hour).UnixMilli()
	closeMs := time.Now().Add(-24 * time.Hour).UnixMilli()
	fake := &fakeMT5HistClient{orders: []*mt5pb.Order{
		{Ticket: 1, Symbol: "XAUUSDm", OrderType: mt5pb.OrderType_OrderType_Buy,
			Swap: -0.14, Lots: 0.01, CloseLots: 0.01, OpenTimestampUTC: openMs, CloseTimestampUTC: closeMs},
		{Ticket: 2, Symbol: "XAUUSDm", OrderType: mt5pb.OrderType_OrderType_Sell,
			Swap: 0.05, Lots: 0.01, CloseLots: 0.01, OpenTimestampUTC: openMs, CloseTimestampUTC: closeMs},
		{Ticket: 3, Symbol: "", OrderType: mt5pb.OrderType_OrderType_Balance,
			Swap: 999, Lots: 1, CloseLots: 1, OpenTimestampUTC: openMs, CloseTimestampUTC: closeMs},
		{Ticket: 4, Symbol: "", OrderType: mt5pb.OrderType_OrderType_Credit,
			Swap: 888, Lots: 1, CloseLots: 1, OpenTimestampUTC: openMs, CloseTimestampUTC: closeMs},
		{Ticket: 5, Symbol: "XAUUSDm", OrderType: mt5pb.OrderType(999),
			Swap: 777, Lots: 1, CloseLots: 1, OpenTimestampUTC: openMs, CloseTimestampUTC: closeMs},
	}}
	scan := mt5OrderHistoryScan(fake, "tok")
	out, err := scan(context.Background(), time.Now().Add(-96*time.Hour), time.Now())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("白名单应只放行 Buy/Sell 2 行，got %d", len(out))
	}
	if out[0].OrderTicket != 1 || out[1].OrderTicket != 2 {
		t.Fatalf("tickets=%v/%v", out[0].OrderTicket, out[1].OrderTicket)
	}
}

// T-I4b MT4：Op_Balance=6/Op_Credit=7 不入结果。
type fakeMT4HistClient struct {
	mt4.MT4Client
	orders []*mt4.Order
}

func (f *fakeMT4HistClient) OrderHistory(ctx context.Context, in *mt4.OrderHistoryRequest, opts ...grpc.CallOption) (*mt4.OrderHistoryReply, error) {
	return &mt4.OrderHistoryReply{Result: f.orders}, nil
}

func TestD427_MT4Scan_OrderTypeWhitelist(t *testing.T) {
	openTS := timestamppb.New(time.Now().Add(-72 * time.Hour))
	closeTS := timestamppb.New(time.Now().Add(-24 * time.Hour))
	fake := &fakeMT4HistClient{orders: []*mt4.Order{
		{Ticket: 1, Symbol: "XAUUSD", Type: mt4.Op_Op_Buy, Swap: -0.1, Lots: 0.01, OpenTime: openTS, CloseTime: closeTS},
		{Ticket: 2, Symbol: "", Type: mt4.Op_Op_Balance, Swap: 999, Lots: 1, OpenTime: openTS, CloseTime: closeTS},
		{Ticket: 3, Symbol: "", Type: mt4.Op_Op_Credit, Swap: 888, Lots: 1, OpenTime: openTS, CloseTime: closeTS},
	}}
	scan := mt4OrderHistoryScan(fake, "tok")
	out, err := scan(context.Background(), time.Now().Add(-96*time.Hour), time.Now())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(out) != 1 || out[0].OrderTicket != 1 {
		t.Fatalf("白名单应只放行 Buy 1 行，got %v", out)
	}
}
