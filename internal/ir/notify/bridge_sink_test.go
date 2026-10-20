package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestBridgeSinkPayload 载荷 schema+鉴权+去重——S1 定死面断言。
func TestBridgeSinkPayload(t *testing.T) {
	var got atomic.Int32
	var last bridgePayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Bridge-Token") != "tok1" {
			w.WriteHeader(401)
			return
		}
		got.Add(1)
		_ = json.NewDecoder(r.Body).Decode(&last)
		w.WriteHeader(202)
	}))
	defer srv.Close()
	s := NewAgentBridgeSink(
		func() string { return srv.URL },
		func() string { return "tok1" },
		func() []string { return []string{"[前提塌]"} },
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	// critical 全级过
	s.Push(1, "critical", "[safego] panic test")
	// warn 白名单命中过
	s.Push(2, "warn", "[前提塌] test warn")
	// warn 非白名单拒
	s.Push(3, "warn", "random warn 不入桥")
	// info 级拒
	s.Push(4, "info", "info 不入桥")
	time.Sleep(200 * time.Millisecond)
	if got.Load() != 2 {
		t.Fatalf("应 2 POST（critical+warn 白名单）: %d", got.Load())
	}
	if last.AlertID != 2 || last.Level != "warn" || last.DedupHash == "" {
		t.Fatalf("载荷 schema 不符: %+v", last)
	}
	// 24h 去重——同文案再推应丢
	s.Push(5, "critical", "[safego] panic test")
	time.Sleep(100 * time.Millisecond)
	if got.Load() != 2 || s.Dropped != 1 {
		t.Fatalf("同文案 24h 应去重丢弃: posts=%d dropped=%d", got.Load(), s.Dropped)
	}
}

// TestBridgeSinkFailOpen 桥败不阻塞+桥地址缺静默关。
func TestBridgeSinkFailOpen(t *testing.T) {
	// 地址缺=静默关
	s0 := NewAgentBridgeSink(func() string { return "" }, func() string { return "t" },
		func() []string { return nil })
	s0.Push(1, "critical", "x")
	if s0.Posted+s0.Failed+s0.Dropped != 0 {
		t.Fatal("桥地址缺应零 POST 尝试")
	}
	// 桥挂=fail+静默计数（不 panic 不阻塞主径）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	s := NewAgentBridgeSink(func() string { return srv.URL }, func() string { return "t" },
		func() []string { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	s.Push(1, "critical", "x")
	time.Sleep(200 * time.Millisecond)
	if s.Failed != 1 {
		t.Fatalf("桥 500 应记 Failed=1: %d", s.Failed)
	}
}
