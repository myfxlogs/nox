// d037_binance_openedat_test.go —— D-037 验收锚：binance perp openedAt 自 userTrades 推导。
// positionRisk 无开仓时间 → reconcile 时按 positionSide 桶累 signed qty，取「最近一次
// |amt| 0→非0 的成交时刻」；每 symbol|side 一次 + 持仓集变化失效（缓存命中零 HTTP）。
package reconcile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// d037BinanceServer positionRisk 单仓 + userTrades 可变序列；tradesHits 计 userTrades 命中数。
func d037BinanceServer(t *testing.T, posAmt string, trades *[]map[string]any, tradesHits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v2/positionRisk":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"symbol": "XAUUSDT", "positionAmt": posAmt, "entryPrice": "4000",
				"unRealizedProfit": "0", "positionSide": "BOTH", "marginType": "cross",
				"isolatedWallet": "0", "leverage": "10", "notional": "8",
			}})
		case "/fapi/v1/userTrades":
			atomic.AddInt32(tradesHits, 1)
			_ = json.NewEncoder(w).Encode(*trades)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
}

func d037Trade(side, qty string, ms int64) map[string]any {
	return map[string]any{"symbol": "XAUUSDT", "side": side, "positionSide": "BOTH", "qty": qty, "time": ms}
}

// TestD037_BinanceOpenedAtSingleOpen 单次开仓：唯一 SELL 成交 → openedAt=该成交时刻。
func TestD037_BinanceOpenedAtSingleOpen(t *testing.T) {
	var hits int32
	t1 := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC).UnixMilli()
	trades := []map[string]any{d037Trade("SELL", "0.002", t1)}
	srv := d037BinanceServer(t, "-0.002", &trades, &hits)
	defer srv.Close()

	q := NewCEXPositionQuerier("binance", "perp", "key", "secret", "", srv.URL+"/fapi", false)
	orders, err := q.QueryOpenedOrders(context.Background())
	if err != nil {
		t.Fatalf("positions: %v", err)
	}
	if len(orders) != 1 || orders[0].Side != "SELL" {
		t.Fatalf("orders=%+v", orders)
	}
	if !orders[0].OpenedAt.Equal(time.UnixMilli(t1)) {
		t.Fatalf("OpenedAt=%v want %v", orders[0].OpenedAt, time.UnixMilli(t1))
	}
}

// TestD037_BinanceOpenedAtReopen 平后再开：SELL→BUY(归零)→SELL → openedAt=最后 0→非0 时刻（取后者）；
// 且缓存生效——两次 positionRisk 查询只打一次 userTrades。
func TestD037_BinanceOpenedAtReopen(t *testing.T) {
	var hits int32
	t1 := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC).UnixMilli()
	t2 := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC).UnixMilli()
	t3 := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC).UnixMilli()
	trades := []map[string]any{
		d037Trade("SELL", "0.002", t1),
		d037Trade("BUY", "0.002", t2),  // 归零
		d037Trade("SELL", "0.001", t3), // 再开=当前仓开仓点
	}
	srv := d037BinanceServer(t, "-0.001", &trades, &hits)
	defer srv.Close()

	q := NewCEXPositionQuerier("binance", "perp", "key", "secret", "", srv.URL+"/fapi", false)
	orders, err := q.QueryOpenedOrders(context.Background())
	if err != nil {
		t.Fatalf("positions: %v", err)
	}
	if !orders[0].OpenedAt.Equal(time.UnixMilli(t3)) {
		t.Fatalf("OpenedAt=%v want %v（平后再开取后者）", orders[0].OpenedAt, time.UnixMilli(t3))
	}
	// 第二轮：缓存命中 → userTrades 零新调用（禁每帧）。
	if _, err := q.QueryOpenedOrders(context.Background()); err != nil {
		t.Fatalf("positions round2: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("userTrades hits=%d want 1（持仓期缓存命中）", got)
	}
}
