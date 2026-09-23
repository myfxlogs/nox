// d418_mt5_scan_test.go —— D-418 S1：MT5 swap 历史扫描切 OrderHistory（哑管复活）。
// OrderHistoryPagination 网关实测 Unimplemented → 切已实证可用的 OrderHistory；
// 时间戳字段 OpenTimestampUTC/CloseTimestampUTC 为 int64 毫秒（KB mt5-order-history-timestamps-millis）。
package dashboard

import (
	"context"
	"fmt"
	"testing"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"

	"google.golang.org/grpc"
)

type fakeMT5HistClient struct {
	mt5pb.MT5Client // 其他方法 nil-panic（本测试不调）
	orders          []*mt5pb.Order
	err             error
}

func (f *fakeMT5HistClient) OrderHistory(ctx context.Context, in *mt5pb.OrderHistoryRequest, opts ...grpc.CallOption) (*mt5pb.OrderHistoryReply, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &mt5pb.OrderHistoryReply{Result: f.orders}, nil
}

// TestD418_MT5Scan_OrderHistory OrderHistory 命中 → 字段映射正确
// （ticket/swap/symbol/毫秒时间戳/lots/side）。
func TestD418_MT5Scan_OrderHistory(t *testing.T) {
	openMs := time.Now().Add(-72 * time.Hour).UnixMilli()
	closeMs := time.Now().Add(-24 * time.Hour).UnixMilli()
	fake := &fakeMT5HistClient{orders: []*mt5pb.Order{
		{
			Ticket:            2553556210,
			Symbol:            "XAUUSDm",
			OrderType:         mt5pb.OrderType_OrderType_Buy,
			Swap:              -0.14,
			Lots:              0.01,
			CloseLots:         0.01,
			OpenTimestampUTC:  openMs,
			CloseTimestampUTC: closeMs,
			Commission:        -0.35,
			Profit:            2.61,
			ClosePrice:        4316.242,
		},
		{
			Ticket:            99,
			Symbol:            "XAUUSDm",
			OrderType:         mt5pb.OrderType_OrderType_Sell,
			Swap:              0,
			Lots:              0.01,
			OpenTimestampUTC:  openMs,
			CloseTimestampUTC: 0, // 未平仓 → 跳过
		},
	}}
	scan := mt5OrderHistoryScan(fake, "tok")
	out, err := scan(context.Background(), time.Now().Add(-96*time.Hour), time.Now())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("orders=%d, want 1（未平仓跳过）", len(out))
	}
	o := out[0]
	if o.OrderTicket != 2553556210 || o.Symbol != "XAUUSDm" || o.Side != "BUY" {
		t.Errorf("mapping wrong: %+v", o)
	}
	if o.Swap != -0.14 || o.Profit != 2.61 || o.ClosePrice != 4316.242 {
		t.Errorf("numeric fields wrong: %+v", o)
	}
	wantClose := time.UnixMilli(closeMs).UTC()
	if !o.CloseTime.Equal(wantClose) {
		t.Errorf("CloseTime=%v want %v（int64 毫秒，非 timestamppb）", o.CloseTime, wantClose)
	}
	if o.Commission != -0.35 {
		t.Errorf("Commission=%v want −0.35", o.Commission)
	}
}

// TestD418_MT5Scan_Error OrderHistory err → scan 透传（不吞）。
func TestD418_MT5Scan_Error(t *testing.T) {
	fake := &fakeMT5HistClient{err: fmt.Errorf("rpc unavailable")}
	scan := mt5OrderHistoryScan(fake, "tok")
	if _, err := scan(context.Background(), time.Now().Add(-time.Hour), time.Now()); err == nil {
		t.Fatal("OrderHistory err 应透传")
	}
}
