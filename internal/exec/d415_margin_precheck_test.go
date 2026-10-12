// d415_margin_precheck_test.go —— D-415 S1：开仓资金预检 venue 原生校验矩阵。
// T1/T2 MT5 RequiredMargin vs FreeMargin×(1−reserve) 拒/放（pipeline 级断言零下单）
// T3 OKX max-avail-size 张数拒/放 T4 校验调用本身 err→fail-closed
// T5 reservePct=0.20 边界 T6 未实现接口→跳过不拦。
package exec

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/bus"
	"google.golang.org/grpc"
)

// fakeMT5Client mt5pb.MT5Client fake：覆盖 RequiredMargin/AccountSummary +
// D-006 S1 主路径名义取数所需 SymbolParams/GetQuote（cs=1/px=100/lev=1000 默认
// 小额名义 → max(broker,名义/20) 分支不影响旧断言）。
type fakeMT5Client struct {
	mt5pb.MT5Client
	required   float64
	freeMargin float64
	err        error
	// D-006：悲观档取数面（零值→小额默认）。
	cs, px, lev float64
	marginCcy   string
}

func (f *fakeMT5Client) RequiredMargin(ctx context.Context, in *mt5pb.RequiredMarginRequest, opts ...grpc.CallOption) (*mt5pb.RequiredMarginReply, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &mt5pb.RequiredMarginReply{Result: f.required}, nil
}
func (f *fakeMT5Client) AccountSummary(ctx context.Context, in *mt5pb.AccountSummaryRequest, opts ...grpc.CallOption) (*mt5pb.AccountSummaryReply, error) {
	if f.err != nil {
		return nil, f.err
	}
	lev := f.lev
	if lev <= 0 {
		lev = 1000
	}
	return &mt5pb.AccountSummaryReply{Result: &mt5pb.AccountSummary{FreeMargin: f.freeMargin, Leverage: lev}}, nil
}

func (f *fakeMT5Client) GetQuote(ctx context.Context, in *mt5pb.GetQuoteRequest, opts ...grpc.CallOption) (*mt5pb.GetQuoteReply, error) {
	if f.err != nil {
		return nil, f.err
	}
	px := f.px
	if px <= 0 {
		px = 100
	}
	return &mt5pb.GetQuoteReply{Result: &mt5pb.Quote{Bid: px - 0.1, Ask: px}}, nil
}

// SymbolParams D-004 P1#2：MT5Executor.TradeModeNow 即时探针的 fake 落点——
// 常返 Full 放行（margin 预检用例不测 trade_mode 分支）。D-006：SymbolInfo
// 补 cs/marginCcy（默认 1/USD）供主路径名义/20 悲观档取数。
func (f *fakeMT5Client) SymbolParams(ctx context.Context, in *mt5pb.SymbolParamsRequest, opts ...grpc.CallOption) (*mt5pb.SymbolParamsReply, error) {
	if f.err != nil {
		return nil, f.err
	}
	cs, mc := f.cs, f.marginCcy
	if cs <= 0 {
		cs = 1
	}
	if mc == "" {
		mc = "USD"
	}
	return &mt5pb.SymbolParamsReply{Result: &mt5pb.SymbolParams{
		SymbolInfo:  &mt5pb.SymbolInfo{ContractSize: cs, MarginCurrency: mc},
		SymbolGroup: &mt5pb.SymGroup{TradeMode: mt5pb.TradeMode_TradeMode_FullAccess},
	}}, nil
}

// d415Pipeline 组装 DSN pipeline。
func d415Pipeline(t *testing.T, execs ...Executor) *Pipeline {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	for _, e := range execs {
		reg.Register(e)
	}
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.SetContractSizeResolver(permissiveCSResolver{})
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{}}
	return p
}

// TestD415_MTRequiredOverAvailRejects T1：required > FreeMargin×0.8 → pipeline 拒开零下单。
func TestD415_MTRequiredOverAvailRejects(t *testing.T) {
	mtExe := NewMT5Executor("v1", "tok", nil, &fakeMT5Client{required: 90, freeMargin: 100})
	okxExe := &d415CaptureExecutor{venue: "okx:perp"}
	p := d415Pipeline(t, mtExe, okxExe)

	// PERP_FUNDING 型隔离 margin 闸（XASSET_CARRY 会先于下单被 combo 白名单拒，
	// err 非空断言被掩护失去咬合力）。
	_, err := p.Execute(context.Background(), "opp:415", "req-415-1", "PERP_FUNDING",
		[]LegSpec{
			{VenueKey: "v1:mt5", VenueSymbol: "XAUUSDm", Side: "BUY", Qty: 0.01, PriceExpected: 2400},
			{VenueKey: "okx:perp", VenueSymbol: "XAUT-USDT-SWAP", Side: "SELL", Qty: 1, PriceExpected: 2400},
		})
	if err == nil {
		t.Fatal("保证金不足应拒开")
	}
	if okxExe.lastOrder.Symbol != "" {
		t.Errorf("拒开后不得有任何下单, got %+v", okxExe.lastOrder)
	}
}

// TestD415_MTRequiredWithinAvailPasses T2：required ≤ avail×0.8 → 放行到下单。
func TestD415_MTRequiredWithinAvailPasses(t *testing.T) {
	mtExe := NewMT5Executor("v1", "tok", nil, &fakeMT5Client{required: 70, freeMargin: 100})
	if err := mtExe.MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01, Venue: "v1:mt5"}); err != nil {
		t.Fatalf("required≤avail×0.8 应放行: %v", err)
	}
}

// d415CaptureExecutor 捕获 PlaceOrder 入参（断言拒开=零下单/放行=有单）。
type d415CaptureExecutor struct {
	venue     string
	lastOrder Order
}

func (e *d415CaptureExecutor) Key() string { return e.venue }
func (e *d415CaptureExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	e.lastOrder = o
	return Fill{OrderID: "fill-1", Price: 100, Qty: o.Qty}, nil
}
func (e *d415CaptureExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return Fill{}, nil
}

// d415RT OKX http client fake RoundTripper。
type d415RT struct {
	maxSell    string
	specErr    bool
	rpcErr     error
	availShape bool // D-429 T4-A：fake 返 availBuy/availSell 形态（端点回漂探测）
}

func (rt *d415RT) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.rpcErr != nil {
		return nil, rt.rpcErr
	}
	var body string
	switch {
	case strings.Contains(req.URL.Path, "/api/v5/public/instruments"):
		body = `{"code":"0","data":[{"ctVal":"0.01","lotSz":"0.01"}]}`
	case strings.Contains(req.URL.Path, "/api/v5/account/max-size"):
		if rt.specErr {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("oops")), Header: make(http.Header), Request: req}, nil
		}
		if rt.availShape { // D-429 T4-A 反向锚：max-avail-size 形态字段 → maxBuy/maxSell 空串响亮失败
			body = `{"code":"0","data":[{"instId":"XAUT-USDT-SWAP","availBuy":"1514.12","availSell":"1514.12"}]}`
			break
		}
		body = `{"code":"0","data":[{"instId":"XAUT-USDT-SWAP","maxBuy":"` + rt.maxSell + `","maxSell":"` + rt.maxSell + `"}]}`
	default:
		body = `{"code":"0","data":[]}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
}

// TestD415_OKXMaxAvailSize T3：maxSell < sz/0.8 拒开；≥ 放行。
func TestD415_OKXMaxAvailSize(t *testing.T) {
	// qty=0.5 基础币, ctVal=0.01 → szContracts=50。0.8 安全垫: maxSell<62.5 拒, ≥62.5 放。
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", "http://x", false)
	e.client = &http.Client{Transport: &d415RT{maxSell: "60"}}
	if err := e.MarginPrecheck(context.Background(), Order{Symbol: "XAUT-USDT-SWAP", Side: "SELL", Qty: 0.5, Venue: "okx:perp"}); err == nil {
		t.Fatal("maxSell=60 < 50/0.8=62.5 应拒开")
	}
	e.client = &http.Client{Transport: &d415RT{maxSell: "100"}}
	if err := e.MarginPrecheck(context.Background(), Order{Symbol: "XAUT-USDT-SWAP", Side: "SELL", Qty: 0.5, Venue: "okx:perp"}); err != nil {
		t.Fatalf("maxSell=100 ≥ 62.5 应放行: %v", err)
	}
}

// TestD429_AvailShapeFailsClosed T4-A 反向锚：fake 返 max-avail-size 形态字段
// → maxBuy/maxSell="" → "avail=\"\" invalid" 响亮失败（防端点回漂静默）。
func TestD429_AvailShapeFailsClosed(t *testing.T) {
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", "http://x", false)
	e.client = &http.Client{Transport: &d415RT{availShape: true}}
	err := e.MarginPrecheck(context.Background(), Order{Symbol: "XAUT-USDT-SWAP", Side: "SELL", Qty: 0.5, Venue: "okx:perp"})
	if err == nil || !strings.Contains(err.Error(), `avail="" invalid`) {
		t.Fatalf("max-avail-size 形态应响亮失败 avail=\"\" invalid，got %v", err)
	}
}

// TestD429_UnitAnchorContracts T4-B 单位锚：生产实拍 BTC 形态 maxSell="5.38"（张数）
// + szContracts=10（qty=0.1 / ctVal=0.01）→ 拒——钉死「张数 vs 张数」判据；
// v1 修法（availBuy USDT）下 10<1211 会假放行，本锚证明单位错位未上线。
func TestD429_UnitAnchorContracts(t *testing.T) {
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", "http://x", false)
	e.client = &http.Client{Transport: &d415RT{maxSell: "5.38"}}
	err := e.MarginPrecheck(context.Background(), Order{Symbol: "XAUT-USDT-SWAP", Side: "SELL", Qty: 0.1, Venue: "okx:perp"})
	if err == nil {
		t.Fatal("sz=10 张 > maxSell=5.38×0.8=4.30 应拒开")
	}
	if !strings.Contains(err.Error(), "insufficient") {
		t.Fatalf("应走张数比较拒绝，got %v", err)
	}
}

// TestD415_RPCFailClosed T4：venue 校验调用本身 err → fail-closed 拒开（不吞错）。
func TestD415_RPCFailClosed(t *testing.T) {
	mtExe := NewMT5Executor("v1", "tok", nil, &fakeMT5Client{err: errors.New("rpc timeout")})
	if err := mtExe.MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01, Venue: "v1:mt5"}); err == nil {
		t.Fatal("MT5 RPC err 应 fail-closed 拒开")
	}
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", "http://x", false)
	e.client = &http.Client{Transport: &d415RT{specErr: true}}
	if err := e.MarginPrecheck(context.Background(), Order{Symbol: "XAUT-USDT-SWAP", Side: "SELL", Qty: 0.5, Venue: "okx:perp"}); err == nil {
		t.Fatal("OKX 端点 5xx 应 fail-closed 拒开")
	}
}

// TestD415_ReservePctBoundary T5：reservePct=0.20 边界——required=avail×0.79 过、×0.81 拒。
func TestD415_ReservePctBoundary(t *testing.T) {
	pass := NewMT5Executor("v1", "tok", nil, &fakeMT5Client{required: 79, freeMargin: 100})
	if err := pass.MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01, Venue: "v1:mt5"}); err != nil {
		t.Fatalf("required=79 ≤ 100×0.8 应放行: %v", err)
	}
	rej := NewMT5Executor("v1", "tok", nil, &fakeMT5Client{required: 81, freeMargin: 100})
	if err := rej.MarginPrecheck(context.Background(), Order{Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01, Venue: "v1:mt5"}); err == nil {
		t.Fatal("required=81 > 100×0.8 应拒开")
	}
}

// TestD415_NoInterfaceSkips T6：未实现 MarginPrechecker 的 executor → 跳过不拦（惯例断言）。
func TestD415_NoInterfaceSkips(t *testing.T) {
	var e Executor = &d415CaptureExecutor{venue: "binance:perp"}
	if _, ok := e.(MarginPrechecker); ok {
		t.Fatal("未实现接口的 executor 不得被断言为 MarginPrechecker")
	}
}
