// d427_binance_fee_test.go —— D-427 I3：binance 消解回填费（userTrades commission 聚合）。
package exec

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// T-I3a FILLED 出 Fill → FeeUSD=Σ userTrades.commission（USDT 行）；非 USDT 行跳过不计。
func TestD427BinanceFillFeeFromUserTrades(t *testing.T) {
	var tradesCalls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/positionSide/dual":
			_, _ = io.WriteString(w, `{"dualSidePosition":"true"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/order":
			_, _ = io.WriteString(w, `{"orderId":888,"executedQty":"0"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/order":
			_, _ = io.WriteString(w, `{"status":"FILLED","executedQty":"1.0","avgPrice":"100"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/userTrades":
			atomic.AddInt64(&tradesCalls, 1)
			if r.URL.Query().Get("orderId") != "888" {
				t.Errorf("userTrades orderId=%q want 888", r.URL.Query().Get("orderId"))
			}
			_, _ = io.WriteString(w, `[
				{"commission":"0.04","commissionAsset":"USDT","realizedPnl":"0"},
				{"commission":"0.06","commissionAsset":"USDT","realizedPnl":"0"},
				{"commission":"0.5","commissionAsset":"BNB","realizedPnl":"0"},
				{"commission":"0","commissionAsset":"USDT","realizedPnl":"9.9"}
			]`)
		case r.URL.Path == "/v1/premiumIndex":
			_, _ = io.WriteString(w, `{"markPrice":"0.3342"}`)
			return
		case r.URL.Path == "/v1/exchangeInfo":
			_, _ = io.WriteString(w, `{"symbols":[{"symbol":"BTCUSDT","filters":[{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"}]}]}`)
			return
		case r.URL.Path == "/v1/symbolConfig":
			_, _ = io.WriteString(w, `{"symbol":"BTCUSDT","maxNotionalValue":100000000}`)
			return
		case r.URL.Path == "/v1/premiumIndex":
			_, _ = io.WriteString(w, `{"markPrice":"0.3342"}`)
			return
		case r.URL.Path == "/v1/exchangeInfo":
			_, _ = io.WriteString(w, `{"symbols":[{"symbol":"BTCUSDT","filters":[{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"}]}]}`)
			return
		case r.URL.Path == "/v1/symbolConfig":
			_, _ = io.WriteString(w, `{"symbol":"BTCUSDT","maxNotionalValue":100000000}`)
			return
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL, false)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTCUSDT", Side: "BUY", Qty: 1.0})
	if err != nil {
		t.Fatalf("FILLED: %v", err)
	}
	if atomic.LoadInt64(&tradesCalls) != 1 {
		t.Fatalf("userTrades 查询 1 次，got %d", tradesCalls)
	}
	// 0.04+0.06=0.10（BNB 行跳过；realizedPnl 不计）
	if fill.FeeUSD < 0.099 || fill.FeeUSD > 0.101 {
		t.Fatalf("FeeUSD=ΣUSDT commission=0.10，got %v", fill.FeeUSD)
	}
}

// T-I3b userTrades 查询失败 → FeeUSD=0 不阻断出 Fill（0=诚实缺省语义不变）。
func TestD427BinanceFeeQueryErrZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/positionSide/dual":
			_, _ = io.WriteString(w, `{"dualSidePosition":"true"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/order":
			_, _ = io.WriteString(w, `{"orderId":889,"executedQty":"0"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/order":
			_, _ = io.WriteString(w, `{"status":"FILLED","executedQty":"1.0","avgPrice":"100"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/userTrades":
			w.WriteHeader(500)
		case r.URL.Path == "/v1/premiumIndex":
			_, _ = io.WriteString(w, `{"markPrice":"0.3342"}`)
			return
		case r.URL.Path == "/v1/exchangeInfo":
			_, _ = io.WriteString(w, `{"symbols":[{"symbol":"BTCUSDT","filters":[{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"}]}]}`)
			return
		case r.URL.Path == "/v1/symbolConfig":
			_, _ = io.WriteString(w, `{"symbol":"BTCUSDT","maxNotionalValue":100000000}`)
			return
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL, false)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTCUSDT", Side: "BUY", Qty: 1.0})
	if err != nil {
		t.Fatalf("费查询失败不得阻断出 Fill: %v", err)
	}
	if fill.FeeUSD != 0 {
		t.Fatalf("费查询失败 FeeUSD=0 诚实缺省，got %v", fill.FeeUSD)
	}
}
