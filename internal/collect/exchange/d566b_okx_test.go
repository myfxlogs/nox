package exchange

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/exec"
)

// D-566 包B 验收锚 A1/A3/A7/A8（A2/A4/A5/A6 在 exec 包）。

// A1：私有订阅 args 6 项——orders/SPOT + liquidation-warning/ANY 在列。
func TestD566B_A1_SubscribeArgs(t *testing.T) {
	args := okxPrivateSubscribeArgs()
	if len(args) != 6 {
		t.Fatalf("args=%d want 6", len(args))
	}
	has := func(ch, instType string) bool {
		for _, a := range args {
			if a["channel"] == ch && a["instType"] == instType {
				return true
			}
		}
		return false
	}
	if !has("orders", "SPOT") {
		t.Fatal("缺 orders/SPOT arg")
	}
	if !has("liquidation-warning", "ANY") {
		t.Fatal("缺 liquidation-warning/ANY arg")
	}
	if !has("orders", "SWAP") || !has("positions", "SWAP") {
		t.Fatal("既有 SWAP args 被破坏")
	}
}

// A3：SPOT orders 帧跳 ctValFor（accFillSz 原生即基础币）；SWAP 仍换算。
func TestD566B_A3_SpotSkipCtVal(t *testing.T) {
	var ctCalls int32
	oc := NewOrderEventCache()
	o := NewOKXPrivateWS("K", "S", "P", false, nil, "okx:spot", nil, oc, NewPositionEventCache(), OKXPrivateHooks{})
	o.SetCtValFor(func(context.Context, string) (float64, error) {
		atomic.AddInt32(&ctCalls, 1)
		return 0.01, nil
	})
	ctx := context.Background()

	// SPOT instId（无 -SWAP 后缀）→ ctValFor 零调用，qty=accFillSz 原值。
	o.handleData(ctx, "orders", nil, []map[string]any{{
		"instId": "BTC-USDT", "ordId": "s1", "state": "filled", "accFillSz": "0.5", "avgPx": "60000",
	}})
	if got := atomic.LoadInt32(&ctCalls); got != 0 {
		t.Fatalf("SPOT ctValFor calls=%d want 0", got)
	}
	ev, ok := oc.get("okx:spot", "s1", "", time.Minute, time.Now())
	if !ok || ev.AccFillQty != 0.5 {
		t.Fatalf("SPOT ev=%+v ok=%v want qty=0.5 原生基础币", ev, ok)
	}

	// SWAP instId → 仍走 ctVal 换算（0.5 张 × 0.01 = 0.005 基础币）。
	o.handleData(ctx, "orders", nil, []map[string]any{{
		"instId": "BTC-USDT-SWAP", "ordId": "s2", "state": "filled", "accFillSz": "0.5", "avgPx": "60000",
	}})
	if got := atomic.LoadInt32(&ctCalls); got != 1 {
		t.Fatalf("SWAP ctValFor calls=%d want 1", got)
	}
	ev, ok = oc.get("okx:spot", "s2", "", time.Minute, time.Now())
	if !ok || ev.AccFillQty != 0.005 {
		t.Fatalf("SWAP ev=%+v ok=%v want qty=0.005（张×ctVal）", ev, ok)
	}
}

// A7：instruments 公共帧 → 规格缓存失效（下次 Load 重取）。
func TestD566B_A7_InstrumentsInvalidate(t *testing.T) {
	instID := "D566WS-USDT-SWAP"
	var gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&gets, 1)
		_, _ = io.WriteString(w, fmt.Sprintf(
			`{"code":"0","data":[{"ctVal":"1","lotSz":"1","maxMktSz":"100","state":"live","instId":"%s"}]}`, instID))
	}))
	defer srv.Close()
	ctx := context.Background()

	if _, _, _, err := exec.OkxSwapSpec(ctx, srv.Client(), srv.URL, instID); err != nil {
		t.Fatalf("prime: %v", err)
	}
	ws := &OKXWebSocket{}
	ws.handleInstruments(wsMsg{Data: []map[string]any{{"instId": instID}}})
	if _, _, _, err := exec.OkxSwapSpec(ctx, srv.Client(), srv.URL, instID); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if got := atomic.LoadInt32(&gets); got != 2 {
		t.Fatalf("instruments GET=%d want 2（WS 失效后重取）", got)
	}
}

// A8：liquidation-warning 帧 → OnLiqWarning 三键透传。
func TestD566B_A8_LiqWarning(t *testing.T) {
	var gotInst, gotPos, gotRatio string
	var calls int32
	o := NewOKXPrivateWS("K", "S", "P", false, nil, "okx:perp", nil, NewOrderEventCache(), NewPositionEventCache(), OKXPrivateHooks{
		OnLiqWarning: func(instID, posSide, mgnRatio string) {
			atomic.AddInt32(&calls, 1)
			gotInst, gotPos, gotRatio = instID, posSide, mgnRatio
		},
	})
	o.handleData(context.Background(), "liquidation-warning", nil, []map[string]any{{
		"instId": "BTC-USDT-SWAP", "posSide": "long", "mgnRatio": "1.2",
	}})
	if atomic.LoadInt32(&calls) != 1 || gotInst != "BTC-USDT-SWAP" || gotPos != "long" || gotRatio != "1.2" {
		t.Fatalf("calls=%d got=%s/%s/%s", calls, gotInst, gotPos, gotRatio)
	}
}
