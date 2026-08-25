// d108_p2_test.go —— D-108 P2 T1+T2 单测（先红后绿）。
// T1：Binance perp hedge 带 positionSide / one-way 带 reduceOnly / OKX reduceOnly 存在 / 探测失败保守处理 / 被拒 critical 告警。
// T2：开仓零成交 → 腿 FAILED / 平仓部分成交 close leg Qty=fill.Qty。
package exec

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// === T1: Binance perp ===

// TestD108P2_BinancePerpHedgePosSide hedge 模式 → 下单带 positionSide（大写 LONG/SHORT）。
func TestD108P2_BinancePerpHedgePosSide(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fapi/v1/positionSide/dual" {
			_, _ = io.WriteString(w, `{"dualSidePosition":true}`)
			return
		}
		if r.URL.Path != "/fapi/v1/order" {
			return // 非 order 请求不检查
		}
		q := r.URL.Query()
		if ps := q.Get("positionSide"); ps != "LONG" {
			t.Errorf("hedge mode: positionSide = %q, want LONG", ps)
		}
		if ro := q.Get("reduceOnly"); ro != "" {
			t.Errorf("hedge mode: reduceOnly should not be set, got %q", ro)
		}
		_, _ = io.WriteString(w, `{"orderId":1,"executedQty":"1.0","fills":[{"price":"50000","qty":"1.0"}]}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL, false)
	_, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTCUSDT", Side: "SELL", Qty: 1, PositionSide: "long"})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
}

// TestD108P2_BinancePerpOneWayReduceOnly one-way 模式 → 下单带 reduceOnly=true（无 positionSide）。
func TestD108P2_BinancePerpOneWayReduceOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fapi/v1/positionSide/dual" {
			_, _ = io.WriteString(w, `{"dualSidePosition":false}`)
			return
		}
		if r.URL.Path != "/fapi/v1/order" {
			return // 非 order 请求（如轮询）不检查
		}
		q := r.URL.Query()
		if ro := q.Get("reduceOnly"); ro != "true" {
			t.Errorf("one-way mode: reduceOnly = %q, want true", ro)
		}
		if ps := q.Get("positionSide"); ps != "" {
			t.Errorf("one-way mode: positionSide should not be set, got %q", ps)
		}
		_, _ = io.WriteString(w, `{"orderId":2,"executedQty":"1.0","fills":[{"price":"50000","qty":"1.0"}]}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL, false)
	_, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTCUSDT", Side: "SELL", Qty: 1, PositionSide: "long"})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
}

// TestD108P2_BinancePerpProbeFailConservative 探测失败 → 保守 hedge（有 positionSide 就传）。
func TestD108P2_BinancePerpProbeFailConservative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fapi/v1/positionSide/dual" {
			http.Error(w, "server error", 500)
			return
		}
		if r.URL.Path != "/fapi/v1/order" {
			return // 非 order 请求不检查
		}
		q := r.URL.Query()
		// 探测失败 → 保守 hedge：有 PositionSide 就传 positionSide
		if ps := q.Get("positionSide"); ps != "LONG" {
			t.Errorf("probe fail conservative: positionSide = %q, want LONG", ps)
		}
		_, _ = io.WriteString(w, `{"orderId":3,"executedQty":"1.0","fills":[{"price":"50000","qty":"1.0"}]}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL, false)
	_, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTCUSDT", Side: "SELL", Qty: 1, PositionSide: "long"})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
}

// TestD108P2_BinancePerpRejectedCritical 被拒 → critical 告警（AlertFn 调用，posID 挂接）。
func TestD108P2_BinancePerpRejectedCritical(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fapi/v1/positionSide/dual" {
			_, _ = io.WriteString(w, `{"dualSidePosition":false}`)
			return
		}
		http.Error(w, `{"code":-2022,"msg":"ReduceOnly Order Rejected"}`, 400)
	}))
	defer srv.Close()

	var alertMsg string
	var alertPosID *int64
	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL, false)
	e.AlertFn = func(posID *int64, level, msg string) {
		alertPosID = posID
		alertMsg = msg
	}
	_, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTCUSDT", Side: "SELL", Qty: 1, PosID: 999})
	if err == nil {
		t.Fatal("expected error for rejected order")
	}
	if alertPosID == nil || *alertPosID != 999 {
		t.Errorf("critical alert posID = %v, want 999", alertPosID)
	}
	if !strings.Contains(alertMsg, "rejected") && !strings.Contains(alertMsg, "cex order") {
		t.Errorf("critical alert msg = %q, want 'rejected' or 'cex order'", alertMsg)
	}
}

// === T1: OKX perp reduceOnly ===

// TestD108P2_OKXPerpReduceOnly OKX perp body 含 reduceOnly:true。
func TestD108P2_OKXPerpReduceOnly(t *testing.T) {
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)
		_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"123","sCode":"0","avgPx":"50000","accFillSz":"1.0"}]}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("okx", "perp", "k", "s", "p", srv.URL, true)
	_, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC-USDT", Side: "SELL", Qty: 1, PositionSide: "long"})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if !strings.Contains(capturedBody, `"reduceOnly":true`) {
		t.Errorf("OKX perp body missing reduceOnly:true: %s", capturedBody)
	}
}

// TestD108P2_OKXPerpRejectedCritical OKX 被拒 → critical 告警。
func TestD108P2_OKXPerpRejectedCritical(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":"1","msg":"All operations failed","data":[{"sCode":"51000","sMsg":"Parameter error"}]}`)
	}))
	defer srv.Close()

	var alertMsg string
	var alertPosID *int64
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", srv.URL, true)
	e.AlertFn = func(posID *int64, level, msg string) {
		alertPosID = posID
		alertMsg = msg
	}
	_, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC-USDT", Side: "SELL", Qty: 1, PosID: 888})
	if err == nil {
		t.Fatal("expected error for rejected OKX order")
	}
	if alertPosID == nil || *alertPosID != 888 {
		t.Errorf("critical alert posID = %v, want 888", alertPosID)
	}
	if !strings.Contains(alertMsg, "okx") {
		t.Errorf("critical alert msg = %q, want 'okx'", alertMsg)
	}
}

// === T2: 开仓零成交 → 腿 FAILED ===

// TestD108P2_ExecuteLegZeroFill 零成交（qty=0/price=0）→ 腿标 FAILED（不假 FILLED）。
func TestD108P2_ExecuteLegZeroFill(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&fakeExecExecutor{venue: "vA:mt5", fill: Fill{OrderID: "T0", Price: 0, Qty: 0}})
	p := &Pipeline{registry: reg}
	r := p.executeLeg(context.Background(), LegSpec{VenueKey: "vA:mt5", Side: "BUY", VenueSymbol: "AUDJPY", Qty: 1.0})
	if r.Status != "FAILED" {
		t.Errorf("zero fill: leg status = %q, want FAILED", r.Status)
	}
}

// TestD108P2_ExecuteLegNormalFill 正常成交 → 腿标 FILLED（不误判）。
func TestD108P2_ExecuteLegNormalFill(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&fakeExecExecutor{venue: "vA:mt5", fill: Fill{OrderID: "T100", Price: 113.5, Qty: 1.0}})
	p := &Pipeline{registry: reg}
	r := p.executeLeg(context.Background(), LegSpec{VenueKey: "vA:mt5", Side: "BUY", VenueSymbol: "AUDJPY", Qty: 1.0})
	if r.Status != "FILLED" {
		t.Errorf("normal fill: leg status = %q, want FILLED", r.Status)
	}
	if r.Qty != 1.0 || r.Price != 113.5 {
		t.Errorf("normal fill: qty=%v price=%v, want 1.0/113.5", r.Qty, r.Price)
	}
}

// === T2: 平仓部分成交 close leg Qty=fill.Qty ===

// TestD108P2_CloseLegPartialFillQty 部分平仓 → close leg Qty 记 fill.Qty（实际成交量）。
// 用真实 PG store：seed position(OPEN) + OPEN leg → ClosePosition（fake executor 返回部分成交 0.3）
// → 验证 CLOSE leg Qty=0.3（fill.Qty），不是 1.0（leg.Qty）。
func TestD108P2_CloseLegPartialFillQty(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// seed position（OPEN，CEX perp venue）
	var posID int64
	err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, expected_net_bps, opened_at)
		VALUES ('SWAPARB:BTCUSDT:vA:vB:1', 'SWAPARB', 'OPEN', 5.0, NOW())
		RETURNING id`).Scan(&posID)
	if err != nil {
		t.Fatalf("seed position: %v", err)
	}
	// seed OPEN leg（qty=1.0, FILLED, CEX perp venue）
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: "vB:perp",
		Symbol: "BTCUSDT", Side: "BUY", Qty: 1.0, OrderID: "T200", Status: "FILLED",
	}); err != nil {
		t.Fatalf("seed leg: %v", err)
	}
	// 构造 Pipeline：fake executor 返回部分成交（fill.Qty=0.3）
	reg := NewRegistry()
	reg.Register(&fakeExecExecutor{venue: "vB:perp", fill: Fill{OrderID: "T201", Price: 50000, Qty: 0.3}})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50})
	// ClosePosition：CEX 腿走 PlaceOrder 反向 → fake executor 返回 fill.Qty=0.3
	_ = p.ClosePosition(ctx, posID) // 部分成交，可能不置 CLOSED（A2 残留回读），但 CLOSE leg 应已写
	// 查 CLOSE leg
	legs, err := s.ListPositionLegs(ctx, posID)
	if err != nil {
		t.Fatalf("list legs: %v", err)
	}
	var closeLeg *pgstore.PositionLeg
	for i := range legs {
		if legs[i].Phase == "CLOSE" {
			closeLeg = &legs[i]
			break
		}
	}
	if closeLeg == nil {
		t.Fatal("no CLOSE leg created")
	}
	if closeLeg.Qty != 0.3 {
		t.Errorf("partial fill: close leg Qty = %v, want 0.3 (fill.Qty)", closeLeg.Qty)
	}
}
