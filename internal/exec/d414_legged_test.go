// d414_legged_test.go —— D-414 §1.6 前置（T5/T6）。
// T5：CEX 腿 PlaceOrder context 超时 → UNKNOWN（旧码 MT 专属=FAILED，红锚）。
//
//	MT 腿超时 → UNKNOWN 回归断言（既有行为不得退化）。
//
// T6：CEX 残腿回收平仓单带 PositionSide（OKX/Binance 适配器据此写 reduceOnly/
//
//	posSide——KB cex-close-reduceonly-missing 漏修先例盯住此缝）。
package exec

import (
	"context"
	"testing"

	"arb/internal/store/pgstore"
)

// d414ErrExecutor PlaceOrder 恒注入错；CloseOrder 记录调用（MT 路径）。
type d414ErrExecutor struct {
	venue     string
	placeErr  error
	lastOrder Order
	called    int
}

func (e *d414ErrExecutor) Key() string { return e.venue }
func (e *d414ErrExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	e.called++
	e.lastOrder = o
	return Fill{}, e.placeErr
}
func (e *d414ErrExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return Fill{}, nil
}

// d414CaptureExecutor 捕获 PlaceOrder 入参（T6 断言 PositionSide/reduceOnly 缝）。
type d414CaptureExecutor struct {
	venue     string
	lastOrder Order
}

func (e *d414CaptureExecutor) Key() string { return e.venue }
func (e *d414CaptureExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	e.lastOrder = o
	return Fill{OrderID: "close-1", Price: 100, Qty: o.Qty}, nil
}
func (e *d414CaptureExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return Fill{}, nil
}

// TestD414_CexTimeoutUnknown T5：CEX 腿超时=UNKNOWN——请求可能已达交易所，
// 假 FAILED 会让自动收编平可见腿后换腿裸奔（500 实案放大路径）。
func TestD414_CexTimeoutUnknown(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&d414ErrExecutor{venue: "okx:perp", placeErr: context.DeadlineExceeded})
	p := &Pipeline{registry: reg}
	p.SetContractSizeResolver(permissiveCSResolver{})

	res := p.executeLeg(context.Background(), LegSpec{
		VenueKey: "okx:perp", VenueSymbol: "XAUT-USDT-SWAP", Side: "SELL", Qty: 1, PriceExpected: 2400,
	}, "cid-414")
	if res.Status != "UNKNOWN" {
		t.Fatalf("CEX 超时须 UNKNOWN（不可假 FAILED）, got %q", res.Status)
	}
}

// TestD414_MtTimeoutUnknownRegression T5 回归：MT 腿超时 UNKNOWN 既有语义不得退化。
func TestD414_MtTimeoutUnknownRegression(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&d414ErrExecutor{venue: "v1:mt5", placeErr: context.DeadlineExceeded})
	p := &Pipeline{registry: reg}
	p.SetContractSizeResolver(permissiveCSResolver{})

	res := p.executeLeg(context.Background(), LegSpec{
		VenueKey: "v1:mt5", VenueSymbol: "XAUUSDm", Side: "BUY", Qty: 0.01, PriceExpected: 2400,
	}, "cid-414mt")
	if res.Status != "UNKNOWN" {
		t.Fatalf("MT 超时 UNKNOWN 回归, got %q", res.Status)
	}
}

// TestD414_CexCloseCarriesPositionSide T6：placeCloseOrder 对 CEX 残腿发的反向单
// 必须带 PositionSide=原持仓侧——适配器 isClose=PositionSide!="" 才写 reduceOnly
// （OKX body）/positionSide+reduceOnly（Binance）。缺=反向开仓裸奔。
func TestD414_CexCloseCarriesPositionSide(t *testing.T) {
	reg := NewRegistry()
	exe := &d414CaptureExecutor{venue: "okx:perp"}
	reg.Register(exe)
	p := &Pipeline{registry: reg}

	buyLeg := pgstore.PositionLeg{LegNo: 1, Venue: "okx:perp", Symbol: "XAUT-USDT-SWAP", Side: "BUY", Qty: 1, Status: "FILLED"}
	if _, err := p.placeCloseOrder(context.Background(), 7, buyLeg, "SELL", 1); err != nil {
		t.Fatalf("placeCloseOrder: %v", err)
	}
	if exe.lastOrder.PositionSide != "long" {
		t.Fatalf("BUY 残腿平仓须 PositionSide=long（适配器据此写 reduceOnly）, got %q", exe.lastOrder.PositionSide)
	}
	if exe.lastOrder.Side != "SELL" || exe.lastOrder.PosID != 7 {
		t.Errorf("平仓单方向/posID 不符: %+v", exe.lastOrder)
	}

	sellLeg := pgstore.PositionLeg{LegNo: 2, Venue: "okx:perp", Symbol: "XAUT-USDT-SWAP", Side: "SELL", Qty: 1, Status: "FILLED"}
	if _, err := p.placeCloseOrder(context.Background(), 7, sellLeg, "BUY", 1); err != nil {
		t.Fatalf("placeCloseOrder sell leg: %v", err)
	}
	if exe.lastOrder.PositionSide != "short" {
		t.Fatalf("SELL 残腿平仓须 PositionSide=short, got %q", exe.lastOrder.PositionSide)
	}
}
