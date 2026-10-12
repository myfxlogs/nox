// d428_margin_estimate_test.go —— D-428：RequiredMargin Unimplemented → 降级杠杆估算。
package exec

import (
	"context"
	"errors"
	"strings"
	"testing"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeMT5MarginClient D-428：全 RPC 可控 fake（RequiredMargin/GetQuote/SymbolParams/AccountSummary）。
type fakeMT5MarginClient struct {
	mt5pb.MT5Client
	required    float64
	rmErr       error  // RequiredMargin 传输层 err
	rmEnvErrMsg string // rm.GetError() 在带错误（非空即返回 error envelope）
	freeMargin  float64
	leverage    float64
	asErr       error
	bid, ask    float64
	quoteErr    error
	cs          float64
	marginCcy   string
	spErr       error
	spMode      mt5pb.TradeMode // D-004 P1#2：SymbolGroup.TradeMode（TradeModeNow 探针用）
	spEnvErrMsg string          // resp.GetError() 在带错误
	spNilResult bool            // Result=nil 空返回
}

func (f *fakeMT5MarginClient) RequiredMargin(ctx context.Context, in *mt5pb.RequiredMarginRequest, opts ...grpc.CallOption) (*mt5pb.RequiredMarginReply, error) {
	if f.rmErr != nil {
		return nil, f.rmErr
	}
	if f.rmEnvErrMsg != "" {
		return &mt5pb.RequiredMarginReply{Error: &mt5pb.Error{Message: f.rmEnvErrMsg}}, nil
	}
	return &mt5pb.RequiredMarginReply{Result: f.required}, nil
}
func (f *fakeMT5MarginClient) AccountSummary(ctx context.Context, in *mt5pb.AccountSummaryRequest, opts ...grpc.CallOption) (*mt5pb.AccountSummaryReply, error) {
	if f.asErr != nil {
		return nil, f.asErr
	}
	return &mt5pb.AccountSummaryReply{Result: &mt5pb.AccountSummary{FreeMargin: f.freeMargin, Leverage: f.leverage}}, nil
}
func (f *fakeMT5MarginClient) GetQuote(ctx context.Context, in *mt5pb.GetQuoteRequest, opts ...grpc.CallOption) (*mt5pb.GetQuoteReply, error) {
	if f.quoteErr != nil {
		return nil, f.quoteErr
	}
	return &mt5pb.GetQuoteReply{Result: &mt5pb.Quote{Bid: f.bid, Ask: f.ask}}, nil
}
func (f *fakeMT5MarginClient) SymbolParams(ctx context.Context, in *mt5pb.SymbolParamsRequest, opts ...grpc.CallOption) (*mt5pb.SymbolParamsReply, error) {
	if f.spErr != nil {
		return nil, f.spErr
	}
	if f.spEnvErrMsg != "" {
		return &mt5pb.SymbolParamsReply{Error: &mt5pb.Error{Message: f.spEnvErrMsg}}, nil
	}
	if f.spNilResult {
		return &mt5pb.SymbolParamsReply{}, nil
	}
	return &mt5pb.SymbolParamsReply{Result: &mt5pb.SymbolParams{
		SymbolInfo:  &mt5pb.SymbolInfo{ContractSize: f.cs, MarginCurrency: f.marginCcy},
		SymbolGroup: &mt5pb.SymGroup{TradeMode: f.spMode},
	}}, nil
}

func unimplemented() error { return status.Error(codes.Unimplemented, "method not implemented") }

// T1 estimate 放行：XAUUSDm 0.01、lev=2000、ask=4284、cs=100、free=1494 → required≈2.14 < avail≈1195 → nil。
func TestD428_EstimatePasses(t *testing.T) {
	e := NewMT5Executor("v1", "tok", nil, &fakeMT5MarginClient{
		rmErr: unimplemented(), freeMargin: 1494, leverage: 2000,
		bid: 4283.8, ask: 4284.0, cs: 100, marginCcy: "XAU",
	})
	if err := e.MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01}); err != nil {
		t.Fatalf("estimate 应放行（required≈2.14 < avail≈1195），got %v", err)
	}
}

// T2 estimate 拒绝：lots=100 → required≈21418 > avail → err 含 estimate。
func TestD428_EstimateRejects(t *testing.T) {
	e := NewMT5Executor("v1", "tok", nil, &fakeMT5MarginClient{
		rmErr: unimplemented(), freeMargin: 1494, leverage: 2000,
		ask: 4284.0, cs: 100, marginCcy: "XAU",
	})
	err := e.MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 100})
	if err == nil || !strings.Contains(err.Error(), "estimate") {
		t.Fatalf("estimate 拒绝应含 estimate 文案，got %v", err)
	}
}

// T3 非 Unimplemented 传输错误 → fail-closed 不走降级。
func TestD428_NonUnimplementedNoFallback(t *testing.T) {
	e := NewMT5Executor("v1", "tok", nil, &fakeMT5MarginClient{
		rmErr:      status.Error(codes.DeadlineExceeded, "timeout"),
		freeMargin: 1494, leverage: 2000, ask: 4284, cs: 100, marginCcy: "XAU",
	})
	err := e.MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01})
	if err == nil || strings.Contains(err.Error(), "estimate") {
		t.Fatalf("非 Unimplemented 不得走降级，got %v", err)
	}
}

// T4 rm.GetError() 在带错误 → fail-closed 不走降级。
func TestD428_EnvelopeErrNoFallback(t *testing.T) {
	e := NewMT5Executor("v1", "tok", nil, &fakeMT5MarginClient{
		rmEnvErrMsg: "symbol not found",
		freeMargin:  1494, leverage: 2000, ask: 4284, cs: 100, marginCcy: "XAU",
	})
	err := e.MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01})
	if err == nil || strings.Contains(err.Error(), "estimate") {
		t.Fatalf("envelope err 不得走降级，got %v", err)
	}
}

// T5 降级链失败 → fail-closed（GetQuote err / px=0 / cs=0 / AccountSummary err）。
func TestD428_EstimateChainFailClosed(t *testing.T) {
	base := fakeMT5MarginClient{rmErr: unimplemented(), freeMargin: 1494, leverage: 2000, ask: 4284, cs: 100, marginCcy: "XAU"}
	cases := map[string]func(*fakeMT5MarginClient){
		"quoteErr": func(f *fakeMT5MarginClient) { f.quoteErr = errors.New("quote down") },
		"pxZero":   func(f *fakeMT5MarginClient) { f.ask = 0 },
		"csZero":   func(f *fakeMT5MarginClient) { f.cs = 0 },
		"asErr":    func(f *fakeMT5MarginClient) { f.asErr = errors.New("as down") },
		"spErr":    func(f *fakeMT5MarginClient) { f.spErr = errors.New("sp down") },
		"levZero":  func(f *fakeMT5MarginClient) { f.leverage = 0 },
	}
	for name, mut := range cases {
		f := base
		mut(&f)
		e := NewMT5Executor("v1", "tok", nil, &f)
		if err := e.MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01}); err == nil {
			t.Fatalf("%s: 降级链失败应 fail-closed，got nil", name)
		}
	}
}

// T6 marginCcy 闸四态。
func TestD428_MarginCcyGate(t *testing.T) {
	mk := func(mc, sym string) *MT5Executor {
		return NewMT5Executor("v1", "tok", nil, &fakeMT5MarginClient{
			rmErr: unimplemented(), freeMargin: 1494, leverage: 2000,
			ask: 1.1, bid: 1.09, cs: 100000, marginCcy: mc,
		})
	}
	// EUR+EURJPYm：基币命中但报价 JPY 计价（px 非 USD）→ fail-closed
	if err := mk("EUR", "EURJPYm").MarginPrecheck(context.Background(), Order{Symbol: "EURJPYm", Side: "BUY", Qty: 0.01}); err == nil {
		t.Fatal("EURJPYm marginCcy=EUR 应拒（JPY 价混作 USD 折算=单位破口）")
	}
	// TET+BTCUSDTm：第三货币 → fail-closed
	if err := mk("TET", "BTCUSDTm").MarginPrecheck(context.Background(), Order{Symbol: "BTCUSDTm", Side: "BUY", Qty: 0.01}); err == nil {
		t.Fatal("BTCUSDTm marginCcy=TET 应拒（非 USD 非基币）")
	}
	// EUR+EURUSDm：基币+USD 计价 → 放行
	if err := mk("EUR", "EURUSDm").MarginPrecheck(context.Background(), Order{Symbol: "EURUSDm", Side: "BUY", Qty: 0.01}); err != nil {
		t.Fatalf("EURUSDm marginCcy=EUR 应放行，got %v", err)
	}
	// BTC+BTCUSDm：基币+USD 计价 → 放行
	if err := mk("BTC", "BTCUSDm").MarginPrecheck(context.Background(), Order{Symbol: "BTCUSDm", Side: "BUY", Qty: 0.01}); err != nil {
		t.Fatalf("BTCUSDm marginCcy=BTC 应放行，got %v", err)
	}
}

// T7 lev=2e9 哨兵 → D-006 悲观档 effLev=20（不再 rate=0 退化）：qty=100 cs=100
// ask=4284 → required=42840/20=2142 > avail=0.8 → 拒（收紧情景兜底实证）。
func TestD428_InfiniteLeverageSentinel(t *testing.T) {
	e := NewMT5Executor("v1", "tok", nil, &fakeMT5MarginClient{
		rmErr: unimplemented(), freeMargin: 1, leverage: 2e9,
		ask: 4284, cs: 100, marginCcy: "XAU",
	})
	err := e.MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 100})
	if err == nil || !strings.Contains(err.Error(), "estimate") {
		t.Fatalf("悲观档应拒（required=名义/20 超 avail），got %v", err)
	}
}

// T8 RequiredMargin 正常返回 → 原生路径不受影响（required=90 < free×0.8=80 拒；另 required=50 放）。
// D-006：主路径悲观档取数就位（cs=1/ask=100 → 名义/20=0.005 不咬 broker 断言）。
func TestD428_NativePathUnaffected(t *testing.T) {
	mk := func(req float64) *MT5Executor {
		return NewMT5Executor("v1", "tok", nil, &fakeMT5MarginClient{
			required: req, freeMargin: 100, leverage: 2000,
			ask: 100, cs: 1, marginCcy: "XAU",
		})
	}
	if err := mk(90).MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01}); err == nil {
		t.Fatal("原生路径 required>avail 应拒")
	}
	if err := mk(50).MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01}); err != nil {
		t.Fatalf("原生路径 required<avail 应放，got %v", err)
	}
}
