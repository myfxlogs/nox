// d427_rollback_test.go —— D-427 I2：rollback UNKNOWN+OrderID CEX 腿孤儿单出证。
package exec

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// fakeOrphanExecutor D-427：OrderCanceler+OrderStatusQuerier 可注入 fake。
type fakeOrphanExecutor struct {
	venue       string
	cancelCount int32
	queryCount  int32
	placeCount  int32
	lastPlace   Order
	cancelErr   error
	queryState  string
	queryQty    float64
	queryErr    error
}

func (e *fakeOrphanExecutor) Key() string { return e.venue }
func (e *fakeOrphanExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	atomic.AddInt32(&e.placeCount, 1)
	e.lastPlace = o
	return Fill{OrderID: "rollback-place", Qty: o.Qty}, nil
}
func (e *fakeOrphanExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return Fill{OrderID: "rollback-close", Qty: 1}, nil
}
func (e *fakeOrphanExecutor) CancelOrder(ctx context.Context, ref OrderRef) error {
	atomic.AddInt32(&e.cancelCount, 1)
	return e.cancelErr
}
func (e *fakeOrphanExecutor) QueryOrderStatus(ctx context.Context, orderID, clOrdID, symbol string) (string, float64, float64, float64, error) {
	atomic.AddInt32(&e.queryCount, 1)
	return e.queryState, e.queryQty, 100, 0, e.queryErr
}

// T-I2a UNKNOWN+OrderID CEX 腿、终查 qty>0 → cancel + 反向平仓发出（真实部分量回滚）。
func TestD427Rollback_UnknownCexPartialFill(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts CASCADE`); err != nil {
		t.Fatalf("truncate alerts: %v", err)
	}
	reg := NewRegistry()
	exe := &fakeOrphanExecutor{venue: "binance:perp", queryState: "canceled", queryQty: 0.5}
	reg.Register(exe)
	p := &Pipeline{registry: reg, store: s}

	legs := []LegSpec{{VenueKey: "binance:perp", VenueSymbol: "ETHUSDT", Side: "BUY", Qty: 1}}
	results := []LegResult{{VenueKey: "binance:perp", OrderID: "ord-9", Status: "UNKNOWN"}}
	p.rollbackFilledLegs(ctx, 999, legs, results, 0)

	if exe.cancelCount != 1 {
		t.Fatalf("UNKNOWN+OrderID CEX 腿必须 cancel 出证，got cancel=%d", exe.cancelCount)
	}
	if exe.placeCount != 1 {
		t.Fatalf("终查 qty>0 必须反向回滚，got place=%d", exe.placeCount)
	}
	if exe.lastPlace.Side != "SELL" || exe.lastPlace.Qty != 0.5 || exe.lastPlace.PositionSide != "long" {
		t.Fatalf("反向单参数错：%+v", exe.lastPlace)
	}
}

// T-I2b 终查 qty=0 → 确定未成交干净跳过（无反向单）。
func TestD427Rollback_UnknownCexZeroFill(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	reg := NewRegistry()
	exe := &fakeOrphanExecutor{venue: "okx:perp", queryState: "canceled", queryQty: 0}
	reg.Register(exe)
	p := &Pipeline{registry: reg, store: s}

	legs := []LegSpec{{VenueKey: "okx:perp", VenueSymbol: "ETH-USDT-SWAP", Side: "SELL", Qty: 1}}
	results := []LegResult{{VenueKey: "okx:perp", OrderID: "ord-10", Status: "UNKNOWN"}}
	p.rollbackFilledLegs(ctx, 999, legs, results, 0)

	if exe.cancelCount != 1 || exe.queryCount != 1 {
		t.Fatalf("cancel+终查各 1 次，got cancel=%d query=%d", exe.cancelCount, exe.queryCount)
	}
	if exe.placeCount != 0 {
		t.Fatalf("qty=0 不得反向回滚，got place=%d", exe.placeCount)
	}
}

// T-I2c MT UNKNOWN 腿 → 跳过不动作（消解器 owns，不盲平）。
func TestD427Rollback_UnknownMTSkips(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	reg := NewRegistry()
	exe := &fakeOrphanExecutor{venue: "3001157:mt5"}
	reg.Register(exe)
	p := &Pipeline{registry: reg, store: s}

	legs := []LegSpec{{VenueKey: "3001157:mt5", VenueSymbol: "XAUUSDm", Side: "BUY", Qty: 0.01}}
	results := []LegResult{{VenueKey: "3001157:mt5", OrderID: "t-77", Status: "UNKNOWN"}}
	p.rollbackFilledLegs(ctx, 999, legs, results, 0)

	if exe.cancelCount != 0 || exe.placeCount != 0 {
		t.Fatalf("MT UNKNOWN 腿不做 cancel/反向，got cancel=%d place=%d", exe.cancelCount, exe.placeCount)
	}
}

// T-I2d cancel err → 不 panic + critical 告警落库 + 不反向。
func TestD427Rollback_UnknownCancelErrCritical(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts CASCADE`); err != nil {
		t.Fatalf("truncate alerts: %v", err)
	}
	reg := NewRegistry()
	exe := &fakeOrphanExecutor{venue: "binance:perp", cancelErr: errors.New("cancel refused")}
	reg.Register(exe)
	p := &Pipeline{registry: reg, store: s}

	legs := []LegSpec{{VenueKey: "binance:perp", VenueSymbol: "ETHUSDT", Side: "BUY", Qty: 1}}
	results := []LegResult{{VenueKey: "binance:perp", OrderID: "ord-11", Status: "UNKNOWN"}}
	p.rollbackFilledLegs(ctx, 999, legs, results, 0) // 不得 panic

	if exe.placeCount != 0 {
		t.Fatalf("cancel 失败不得反向回滚，got place=%d", exe.placeCount)
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE level='critical'`).Scan(&n); err != nil {
		t.Fatalf("query alerts: %v", err)
	}
	if n == 0 {
		t.Fatal("cancel 失败孤儿未证否 → 必须有 critical 告警")
	}
}
