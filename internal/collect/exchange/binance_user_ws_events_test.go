// binance_user_ws_events_test.go D-584 回归：订单事件 map 精确键提取。
// 根因：struct tag 大小写不敏感——"n"(佣金金额) 撞 "N"(佣金币种)（同族 "i"/"I"、
// "x"/"X"、spot "z"/"Z" 并存），后写覆盖→ParseFloat 崩→整帧丢→OrderEventCache 恒空
// （生产实证：每笔订单 drop field N: parsing "USDT"）。
package exchange

import (
	"context"
	"math"
	"testing"
	"time"
)

func newEvtTestWS(venueType string) *BinanceUserWS {
	return NewBinanceUserWS("K", "S", "https://unused.invalid", venueType, "binance:"+venueType, false, nil, nil,
		NewOrderEventCache(), NewPositionEventCache(), BinanceUserHooks{})
}

func feeEq(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// TestOrderUpdateNvsN 真帧形（生产实测键序）：n(金额) 在前、N(币种) 在后——
// 修前 tag "n" 被 "USDT" 覆盖 → ParseFloat 崩丢帧（PeekOrder miss）；修后精确键命中。
func TestOrderUpdateNvsN(t *testing.T) {
	b := newEvtTestWS("perp")
	frame := `{"e":"ORDER_TRADE_UPDATE","o":{"s":"TRXUSDT","c":"arb-1","S":"BUY","o":"MARKET","f":"GTC","q":"16","p":"0","ap":"0.334","x":"TRADE","X":"FILLED","i":8886774,"l":"16","z":"16","L":"0.334","n":"0.00267272","N":"USDT","T":1759000000000,"t":1,"b":"0","a":"5.344","m":false,"R":false}}`
	b.handleFrame(context.Background(), []byte(frame))
	ev, ok := b.orderCache.PeekOrder("binance:perp", "8886774")
	if !ok {
		t.Fatal("PeekOrder(orderID) miss——n/N 碰撞丢帧仍在")
	}
	if ev.State != "filled" || ev.AccFillQty != 16 || !feeEq(ev.AvgPx, 0.334) || !feeEq(ev.FeeUSD, 0.00267272) {
		t.Fatalf("event want {filled 16 0.334 0.00267272}, got %+v", ev)
	}
	if ev2, ok := b.orderCache.PeekOrder("binance:perp", "arb-1"); !ok || ev2.State != "filled" {
		t.Fatalf("PeekOrder(clOrdID) miss: ok=%v ev=%+v", ok, ev2)
	}
}

// TestOrderUpdateFieldOrder 同帧键序打乱（N 在 n 前）：精确键提取与键序无关，
// 命中且字段同值（大小写敏感 + 序无关双证）。
func TestOrderUpdateFieldOrder(t *testing.T) {
	b := newEvtTestWS("perp")
	frame := `{"e":"ORDER_TRADE_UPDATE","o":{"N":"USDT","n":"0.00267272","X":"FILLED","x":"TRADE","i":8886775,"c":"arb-2","z":"16","L":"0.334","ap":"0.334"}}`
	b.handleFrame(context.Background(), []byte(frame))
	ev, ok := b.orderCache.PeekOrder("binance:perp", "8886775")
	if !ok {
		t.Fatal("PeekOrder miss——键序翻转不应影响精确键提取")
	}
	if ev.State != "filled" || ev.AccFillQty != 16 || !feeEq(ev.AvgPx, 0.334) || !feeEq(ev.FeeUSD, 0.00267272) {
		t.Fatalf("event want {filled 16 0.334 0.00267272}, got %+v", ev)
	}
}

// TestSpotExecutionReportNvsN spot executionReport 同雷帧（官方 schema 键序：
// I(ExecutionId) 在 i(OrderId) 前、Z(累计报价额) 在 z(累计量) 后、N(币种) 在 n(金额) 后）
// ——修前 z 被 Z 覆盖 33.3、n 被 N 覆盖 "USDT" 崩丢帧；修后精确键命中。
func TestSpotExecutionReportNvsN(t *testing.T) {
	b := newEvtTestWS("spot")
	frame := `{"e":"executionReport","s":"TRXUSDT","c":"spot-1","S":"BUY","o":"LIMIT","f":"GTC","q":"100.00000000","p":"0.33300000","x":"TRADE","X":"FILLED","I":8641984,"l":"100.00000000","z":"100.00000000","L":"0.33300000","n":"0.00267272","N":"USDT","T":1759000000000,"t":305,"i":17959,"w":true,"m":true,"O":1759000000000,"Z":"33.30000000","Y":"33.30000000"}`
	b.handleFrame(context.Background(), []byte(frame))
	ev, ok := b.orderCache.PeekOrder("binance:spot", "17959")
	if !ok {
		t.Fatal("PeekOrder(spot orderID) miss——spot 同型碰撞丢帧仍在")
	}
	if ev.State != "filled" || ev.AccFillQty != 100 || !feeEq(ev.AvgPx, 0.333) || !feeEq(ev.FeeUSD, 0.00267272) {
		t.Fatalf("event want {filled 100 0.333 0.00267272}, got %+v", ev)
	}
}

// TestAccountUpdateZeroPa a.P pa=0 幻影仓过滤（b51a8c61 增收；bntesttrade 实证：
// venue 平仓后推零量行，现状照建 Volume=0 venue.Order → 实持仓表出现已平符号行）。
// 全量替换语义下 pa=0=该 symbol 无仓，不建仓即正确。
func TestAccountUpdateZeroPa(t *testing.T) {
	b := newEvtTestWS("perp")
	open := `{"e":"ACCOUNT_UPDATE","a":{"m":"ORDER","B":[],"P":[{"s":"TRXUSDT","pa":"16","ep":"0.334","iw":"5.3","ps":"BOTH","up":"1.2"}]}}`
	b.handleFrame(context.Background(), []byte(open))
	if poss, ok := b.posCache.get("binance:perp", EventFreshTTL, time.Now()); !ok || len(poss) != 1 {
		t.Fatalf("open frame want 1 position, ok=%v n=%d", ok, len(poss))
	}
	close := `{"e":"ACCOUNT_UPDATE","a":{"m":"ORDER","B":[],"P":[{"s":"TRXUSDT","pa":"0","ep":"0.334","iw":"0","ps":"BOTH","up":"0"}]}}`
	b.handleFrame(context.Background(), []byte(close))
	if poss, _ := b.posCache.get("binance:perp", EventFreshTTL, time.Now()); len(poss) != 0 {
		t.Fatalf("pa=0 行不得建仓（幻影仓过滤）, got %+v", poss)
	}
}
