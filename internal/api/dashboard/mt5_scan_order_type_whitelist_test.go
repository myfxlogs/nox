// d427_ordertype_test.go —— D-427 I4：swap 历史扫描 OrderType 白名单（Balance/Credit/未知跳过）。
package dashboard

import (
	"context"
	"testing"
	"time"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
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
