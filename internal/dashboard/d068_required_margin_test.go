// d068_required_margin_test.go —— D-068 验收锚：MT5 仓位级保证金=RequiredMargin RPC 原生值。
// 主径：vs.reqMargin（reconcile 同批缓存）直显、flag=false（RPC 优先于估算输入）；
// 降级径：RPC 缺键 → D-036 规格估算（flag=true）；同批 (symbol,side,lots) 恰调一次
// （缓存命中/失败均不重复调）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"arb/internal/venue"
)

func d068Order(ticket string, side string, lots float64) venue.Order {
	return venue.Order{
		Venue: "3001157", VenueKey: "3001157:mt5", Ticket: ticket,
		Symbol: "XAUUSDm", Side: side, Volume: lots, OpenPrice: 4237.56,
	}
}

// TestD068_RpcNativeOverridesEstimate RPC 原生值直显（估算输入在也优先 RPC，flag=false）。
func TestD068_RpcNativeOverridesEstimate(t *testing.T) {
	m := NewBrokerPositionMonitor(nil, nil, 0, nil, ServerTZCache{}, nil, nil)
	ord := d068Order("t1", "BUY", 0.03)
	vs := venueState{
		orders:     map[string]venue.Order{"t1": ord},
		marginSpec: map[string]symMarginSpec{"XAUUSDm": {contractSize: 100}},
		imr:        0.0005, imrOK: true,
		reqMargin: map[string]float64{"t1": 6.35634},
	}
	pb := m.protoFor(context.Background(), "3001157:mt5", vs, ord, time.Now())
	if pb.Margin != 6.35634 {
		t.Fatalf("RPC 原生 margin=%v want 6.35634", pb.Margin)
	}
	if pb.MarginIsEstimate {
		t.Fatal("RPC 原生值不应置估算标记")
	}
}

// TestD068_RpcMissDegradesToEstimate RPC 缺键 → D-036 规格估算降级（flag=true，
// 0.03×100×4237.56×0.0005=6.35634）；规格也缺 → 诚实空态（margin=0、flag=false）。
func TestD068_RpcMissDegradesToEstimate(t *testing.T) {
	m := NewBrokerPositionMonitor(nil, nil, 0, nil, ServerTZCache{}, nil, nil)
	ord := d068Order("t1", "BUY", 0.03)
	vs := venueState{
		orders:     map[string]venue.Order{"t1": ord},
		marginSpec: map[string]symMarginSpec{"XAUUSDm": {contractSize: 100}},
		imr:        0.0005, imrOK: true,
	}
	pb := m.protoFor(context.Background(), "3001157:mt5", vs, ord, time.Now())
	if pb.Margin != 6.35634 || !pb.MarginIsEstimate {
		t.Fatalf("降级 margin=%v flag=%v want 6.35634/true", pb.Margin, pb.MarginIsEstimate)
	}
	vs.marginSpec = nil
	pb = m.protoFor(context.Background(), "3001157:mt5", vs, ord, time.Now())
	if pb.Margin != 0 || pb.MarginIsEstimate {
		t.Fatalf("空态 margin=%v flag=%v want 0/false", pb.Margin, pb.MarginIsEstimate)
	}
}

// TestD068_ReconcileBatchDedup prepareVenueView 同批 (symbol,side,lots) 恰调一次：
// 双仓同键共享一次 RPC（值落全部同键 ticket），不同手数/方向各调一次；键不匹配的
// 仓不误传（side 归一化进 RPC 入参）。
func TestD068_ReconcileBatchDedup(t *testing.T) {
	bm := NewBrokerManager(nil, nil, nil, 0)
	calls := map[string]int{}
	bm.attach("3001157", &running{requiredMargin: func(_ context.Context, symbol string, lots float64, side string) (float64, error) {
		if symbol != "XAUUSDm" {
			return 0, fmt.Errorf("unexpected symbol %s", symbol)
		}
		calls[side+"|"+fmt.Sprintf("%v", lots)]++
		return lots * 100 * 4162.2 * 0.0005, nil
	}})
	m := NewBrokerPositionMonitor(bm, nil, 0, nil, ServerTZCache{}, nil, nil)
	orders := []venue.Order{
		d068Order("t1", "BUY", 0.03),
		d068Order("t2", "BUY", 0.03),
		d068Order("t3", "SELL", 0.05),
	}
	vs := m.prepareVenueView(context.Background(), "3001157:mt5", orders, true)
	if len(calls) != 2 || calls["BUY|0.03"] != 1 || calls["SELL|0.05"] != 1 {
		t.Fatalf("RPC 调用=%v want BUY|0.03:1 SELL|0.05:1（同键去重）", calls)
	}
	if vs.reqMargin["t1"] == 0 || vs.reqMargin["t1"] != vs.reqMargin["t2"] {
		t.Fatalf("同键双仓应共享值 t1=%v t2=%v", vs.reqMargin["t1"], vs.reqMargin["t2"])
	}
	if want := 0.05 * 100 * 4162.2 * 0.0005; vs.reqMargin["t3"] != want {
		t.Fatalf("t3=%v want %v", vs.reqMargin["t3"], want)
	}
}

// TestD068_ReconcileFailureNoRetry 同批同键 RPC 失败：不重试、不落键（全部同键仓走
// 降级），跨仓无重复调用。
func TestD068_ReconcileFailureNoRetry(t *testing.T) {
	bm := NewBrokerManager(nil, nil, nil, 0)
	n := 0
	bm.attach("3001157", &running{requiredMargin: func(context.Context, string, float64, string) (float64, error) {
		n++
		return 0, errors.New("gateway down")
	}})
	m := NewBrokerPositionMonitor(bm, nil, 0, nil, ServerTZCache{}, nil, nil)
	orders := []venue.Order{d068Order("t1", "BUY", 0.03), d068Order("t2", "BUY", 0.03)}
	vs := m.prepareVenueView(context.Background(), "3001157:mt5", orders, true)
	if n != 1 {
		t.Fatalf("失败键被重复调 n=%d want 1", n)
	}
	if len(vs.reqMargin) != 0 {
		t.Fatalf("失败不应落键 reqMargin=%v", vs.reqMargin)
	}
}
