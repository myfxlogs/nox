package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMetaApiAdapter_DisabledByDefault D-146 S2：开关默认关，未配置 token 时方法返回 ErrNotConfigured。
func TestMetaApiAdapter_DisabledByDefault(t *testing.T) {
	a := NewMetaApiAdapter("ic", "")
	if a.Enabled() {
		t.Fatal("Enabled() = true, want false (default off)")
	}
	if _, err := a.AccountSummary(context.Background()); err != ErrMetaApiNotConfigured {
		t.Fatalf("AccountSummary err = %v, want ErrMetaApiNotConfigured", err)
	}
	if _, err := a.OpenedOrders(context.Background()); err != ErrMetaApiNotConfigured {
		t.Fatalf("OpenedOrders err = %v, want ErrMetaApiNotConfigured", err)
	}
}

// TestMetaApiAdapter_AccountSummary D-146 S2：mock REST 解析账户摘要。
func TestMetaApiAdapter_AccountSummary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"balance":    10000.50,
			"equity":     10050.25,
			"margin":     500.0,
			"freeMargin": 9550.25,
			"leverage":   100,
			"currency":   "USD",
		})
	}))
	defer srv.Close()

	a := NewMetaApiAdapter("ic", "test-token")
	a.baseURL = srv.URL
	if !a.Enabled() {
		t.Fatal("Enabled() = false with token, want true")
	}
	sum, err := a.AccountSummary(context.Background())
	if err != nil {
		t.Fatalf("AccountSummary: %v", err)
	}
	if sum.Balance != 10000.50 {
		t.Errorf("Balance = %v, want 10000.50", sum.Balance)
	}
	if sum.Equity != 10050.25 {
		t.Errorf("Equity = %v, want 10050.25", sum.Equity)
	}
	if sum.Leverage != 100 {
		t.Errorf("Leverage = %v, want 100", sum.Leverage)
	}
	if sum.Currency != "USD" {
		t.Errorf("Currency = %v, want USD", sum.Currency)
	}
}

// TestMetaApiAdapter_OpenedOrders D-146 S2：mock REST 解析持仓列表。
func TestMetaApiAdapter_OpenedOrders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "12345", "symbol": "AUDJPY", "type": "POSITION_TYPE_BUY", "volume": 0.06, "openPrice": 107.50, "profit": 12.34},
			{"id": "12346", "symbol": "EURUSD", "type": "POSITION_TYPE_SELL", "volume": 0.10, "openPrice": 1.1000, "profit": -5.67},
		})
	}))
	defer srv.Close()

	a := NewMetaApiAdapter("ic", "test-token")
	a.baseURL = srv.URL
	orders, err := a.OpenedOrders(context.Background())
	if err != nil {
		t.Fatalf("OpenedOrders: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("len(orders) = %d, want 2", len(orders))
	}
	if orders[0].Ticket != "12345" || orders[0].Side != "BUY" || orders[0].Volume != 0.06 {
		t.Errorf("order[0] = %+v, want ticket=12345 side=BUY volume=0.06", orders[0])
	}
	if orders[1].Ticket != "12346" || orders[1].Side != "SELL" || orders[1].OpenPrice != 1.1000 {
		t.Errorf("order[1] = %+v, want ticket=12346 side=SELL openPrice=1.1000", orders[1])
	}
}

// TestMetaApiAdapter_QuoteSnapshot D-146 S2：mock REST 解析行情快照。
func TestMetaApiAdapter_QuoteSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"symbol": "AUDJPY",
			"bid":    107.48,
			"ask":    107.52,
			"time":   "2026-08-29T10:00:00Z",
		})
	}))
	defer srv.Close()

	a := NewMetaApiAdapter("ic", "test-token")
	a.baseURL = srv.URL
	q, err := a.QuoteSnapshot(context.Background(), "AUDJPY")
	if err != nil {
		t.Fatalf("QuoteSnapshot: %v", err)
	}
	if q.Symbol != "AUDJPY" || q.Bid != 107.48 || q.Ask != 107.52 {
		t.Errorf("Quote = %+v, want AUDJPY bid=107.48 ask=107.52", q)
	}
}
