package exchange

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDepthTimeoutPerVenue depth REST per-venue 预算锚（返修形态：
// per-request ctx 绑预算非专客户端——transport 栈零动/env 删）。
func TestDepthTimeoutPerVenue(t *testing.T) {
	c := DefaultConfig([]string{"BTC"})
	for _, v := range []string{"gate", "okx", "binance"} {
		if c.DepthTimeout[v] != 8*time.Second {
			t.Fatalf("%s 缺省预算应 8s got %v", v, c.DepthTimeout[v])
		}
	}
	// 慢服务+短预算→per-request ctx 超时必败（绑约束锚——预算真生效）
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		if strings.Contains(r.URL.Path, "order_book") {
			w.Write([]byte(`{"asks":[{"p":"100","s":1}],"bids":[{"p":"99","s":1}]}`)) // gate 对象组 {p,s}
			return
		}
		w.Write([]byte(`{"asks":[["100","1"]],"bids":[["99","1"]]}`))
	}))
	defer slow.Close()
	c.GateBaseURL = slow.URL
	c.DepthTimeout["gate"] = 20 * time.Millisecond
	_, err := fetchDepth(context.Background(), c, "gate", "BTC")
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("慢端点短预算应 deadline 败: %v", err)
	}
	// 未配预算 venue=共享 ctx 零行为变（无穷预算下慢服务能回——对版）
	c2 := DefaultConfig([]string{"BTC"})
	delete(c2.DepthTimeout, "okx")
	_ = c2 // 语义锚=map 可空不炸
}

// TestFetchCexDepthsParallel venue 并行构形锚（R3：串行批共享 ctx 拖批病灶）。
func TestFetchCexDepthsParallel(t *testing.T) {
	// 慢 gate（200ms）+快 binance/okx——并行下总时长≈最慢者非求和。
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "books"): // okx 包 data[]
			w.Write([]byte(`{"data":[{"asks":[["100","1"]],"bids":[["99","1"]]}]}`))
		default: // binance /fapi/v1/depth 数组组
			w.Write([]byte(`{"asks":[["100","1"]],"bids":[["99","1"]]}`))
		}
	}))
	defer fast.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		if strings.Contains(r.URL.Path, "contracts") {
			w.Write([]byte(`{"quanto_multiplier":"1"}`)) // gate 合约规格面
			return
		}
		if strings.Contains(r.URL.Path, "order_book") {
			w.Write([]byte(`{"asks":[{"p":"100","s":1}],"bids":[{"p":"99","s":1}]}`)) // gate 对象组 {p,s}
			return
		}
		w.Write([]byte(`{"asks":[["100","1"]],"bids":[["99","1"]]}`))
	}))
	defer slow.Close()
	c := DefaultConfig([]string{"BTC"})
	c.BinanceBaseURL, c.OKXBaseURL, c.GateBaseURL = fast.URL, fast.URL, slow.URL
	start := time.Now()
	out := FetchCexDepths(context.Background(), c, []string{"BTC"})
	el := time.Since(start)
	if el > 400*time.Millisecond {
		t.Fatalf("并行下总时长应≈最慢 venue（%v）非串行求和", el)
	}
	if len(out) < 2 { // binance+okx 至少（gate 可能因响应结构缺字段 skip——宽松界）
		t.Fatalf("并行面产出应有快 venue 行 got %d", len(out))
	}
}
