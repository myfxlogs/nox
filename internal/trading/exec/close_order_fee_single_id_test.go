// d069_fee_metadata_test.go —— D-069 锚：orderHistoryCommission 不得二次 Append
// id metadata（调用方 ctx 已携；重复值→网关 InvalidArgument，10-05 实拍）。
package exec

import (
	"context"
	"testing"

	mt5pb "arb/internal/data/adapter/gen/mtapi/mt5"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// fakeTrading TradingClient fake：只覆写 OrderClose（平仓成功回执）。
type fakeTrading struct {
	mt5pb.TradingClient
}

func (f *fakeTrading) OrderClose(ctx context.Context, in *mt5pb.OrderCloseRequest, opts ...grpc.CallOption) (*mt5pb.OrderCloseReply, error) {
	return &mt5pb.OrderCloseReply{Result: &mt5pb.Order{CloseLots: 0.03, ClosePrice: 4160}}, nil
}

// fakeMT5 MT5Client fake：OrderHistory 存下 outgoing metadata 供断言。
type fakeMT5 struct {
	mt5pb.MT5Client
	md metadata.MD
}

func (f *fakeMT5) SymbolParams(ctx context.Context, in *mt5pb.SymbolParamsRequest, opts ...grpc.CallOption) (*mt5pb.SymbolParamsReply, error) {
	f.md, _ = metadata.FromOutgoingContext(ctx)
	return &mt5pb.SymbolParamsReply{Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{TradeMode: mt5pb.TradeMode_TradeMode_FullAccess}}}, nil
}

func (f *fakeMT5) OrderHistory(ctx context.Context, in *mt5pb.OrderHistoryRequest, opts ...grpc.CallOption) (*mt5pb.OrderHistoryReply, error) {
	f.md, _ = metadata.FromOutgoingContext(ctx)
	return &mt5pb.OrderHistoryReply{}, nil
}

// TestD069_CloseOrderFeeSingleID CloseOrder → orderHistoryCommission 路径：
// OrderHistory 请求的 outgoing metadata id 恰一个值 "tok"（修复前为 ["tok","tok"]）。
// PlaceOrder 共用同一函数与同形态 Append（mt5_executor.go:148），此处只锚 CloseOrder。
func TestD069_CloseOrderFeeSingleID(t *testing.T) {
	fm := &fakeMT5{}
	e := NewMT5Executor("vA", "tok", &fakeTrading{}, fm)

	fill, err := e.CloseOrder(context.Background(), OrderRef{OrderID: "123"})
	if err != nil {
		t.Fatalf("CloseOrder: %v", err)
	}
	if fill.Qty != 0.03 || fill.Price != 4160 {
		t.Errorf("fill = %+v, want Qty=0.03 Price=4160", fill)
	}
	ids := fm.md.Get("id")
	if len(ids) != 1 || ids[0] != "tok" {
		t.Errorf("outgoing metadata id = %v, want 恰 1 个值 [tok]", ids)
	}
}

// TestD082_TradeModeNowCarriesID TradeModeNow → SymbolParams 的 outgoing metadata 须恰携
// 一个 id="tok"（修复前零头：网关回空 result，首仓 10-06 00:19Z 被 trade_mode unknown 拒）。
func TestD082_TradeModeNowCarriesID(t *testing.T) {
	fm := &fakeMT5{}
	e := NewMT5Executor("vA", "tok", &fakeTrading{}, fm)
	if _, err := e.TradeModeNow(context.Background(), "XAUUSDm"); err != nil {
		t.Fatalf("TradeModeNow: %v", err)
	}
	if ids := fm.md.Get("id"); len(ids) != 1 || ids[0] != "tok" {
		t.Fatalf("outgoing id = %v, want exactly [tok]", ids)
	}
}
