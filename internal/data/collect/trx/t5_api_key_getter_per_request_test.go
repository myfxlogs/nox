package trx

// 验收锚：apiKey getter 每请求现取（fake getter 计数）+热值透传 header。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestD545_T5_ApiKeyGetterPerRequest(t *testing.T) {
	var calls atomic.Int32
	var gotKeys atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKeys.Store(r.Header.Get("TRON-PRO-API-KEY"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"withdraw_amount":12345}`))
	}))
	defer srv.Close()

	var key atomic.Value
	key.Store("key-v1")
	getter := func() string {
		calls.Add(1)
		return key.Load().(string)
	}
	c := NewTxsCollector("addr", nil, srv.URL, getter, "", nil, 0)

	if n := c.fetchWithdrawAmount(context.Background(), "tx1"); n != 12345 {
		t.Fatalf("fetchWithdrawAmount=%d", n)
	}
	if calls.Load() != 1 {
		t.Fatalf("getter 应每请求恰被调一次, got %d", calls.Load())
	}
	if gotKeys.Load().(string) != "key-v1" {
		t.Fatalf("header 未透传 v1: %v", gotKeys.Load())
	}

	// 热更后第二请求携新值（UpdateAppSecret→resolver.Set 等价路径）。
	key.Store("key-v2")
	if n := c.fetchWithdrawAmount(context.Background(), "tx2"); n != 12345 {
		t.Fatalf("fetchWithdrawAmount=%d", n)
	}
	if calls.Load() != 2 {
		t.Fatalf("getter 累计=%d", calls.Load())
	}
	if gotKeys.Load().(string) != "key-v2" {
		t.Fatalf("header 未携热值 v2: %v", gotKeys.Load())
	}
}
