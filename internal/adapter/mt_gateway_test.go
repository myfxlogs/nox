// mt_gateway_test.go —— D-104 P2 U3（2026-08-25）：网关 failover + 断流 episode 告警单测。
// 三用例（先红后绿）：
//   - DialGateways_FallbackOrder：首节点 dial 失败 → 次节点成功（断言 targets 按序试 + 返 nil err）；
//   - DialGateways_AllFail：全部 targets 失败 → 返 err（含最后 err 包装）；
//   - QuoteReconnectBackoff_EpisodeDedup：封顶档只告警一次（quoteAlerted 去重）+ 成功帧后重置可再告。
package adapter

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// --- U3-1: dial fallback 顺序 ---

func TestDialGateways_FallbackOrder(t *testing.T) {
	// 本地 gRPC listener 作为"成功"目标（insecure creds，不关心 RPC 服务）。
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	go srv.Serve(lis)
	defer srv.Stop()
	successTarget := lis.Addr().String()

	var mu sync.Mutex
	var calls []string
	origDial := dialContext
	dialContext = func(ctx context.Context, target string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
		mu.Lock()
		calls = append(calls, target)
		mu.Unlock()
		if target == "fail.example:443" {
			return nil, errors.New("connection refused")
		}
		// 成功目标：用 insecure creds 直 dial 本地 listener（忽略 opts 中的 TLS）。
		return grpc.DialContext(ctx, target, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	}
	defer func() { dialContext = origDial }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialGateways(ctx, "mt5", "v1", []string{"fail.example:443", successTarget})
	if err != nil {
		t.Fatalf("dialGateways: expected success, got %v", err)
	}
	defer conn.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 dial calls, got %d: %v", len(calls), calls)
	}
	if calls[0] != "fail.example:443" {
		t.Fatalf("expected first call to fail.example:443, got %s", calls[0])
	}
	if calls[1] != successTarget {
		t.Fatalf("expected second call to %s, got %s", successTarget, calls[1])
	}
}

// --- U3-2: 全部失败 ---

func TestDialGateways_AllFail(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	origDial := dialContext
	dialContext = func(ctx context.Context, target string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
		mu.Lock()
		calls = append(calls, target)
		mu.Unlock()
		return nil, errors.New("connection refused: " + target)
	}
	defer func() { dialContext = origDial }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	targets := []string{"g1:443", "g2:443", "g3:443"}
	_, err := dialGateways(ctx, "mt5", "v1", targets)
	if err == nil {
		t.Fatal("dialGateways: expected error, got nil")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("expected 3 dial calls (all tried), got %d: %v", len(calls), calls)
	}
	// 错误消息应含最后 err（g3）
	if !contains(err.Error(), "g3:443") {
		t.Fatalf("expected error to contain last target g3:443, got: %s", err.Error())
	}
}

// --- U3-3: episode 告警去重 ---

func TestQuoteReconnectBackoff_EpisodeDedup(t *testing.T) {
	// 缩短退避序列（3 档：1ms/1ms/1ms），快速触达封顶。
	restore := withShortQuoteBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond})
	defer restore()

	var mu sync.Mutex
	var alerts []string
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	a := &MT5Adapter{venue: "vd"}
	a.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}

	// marginReconnectBackoff 长度=3，封顶档 idx>=3。
	// 前 3 次（idx 0,1,2）不告警；第 4 次（idx=3=封顶）告警 1 次；第 5/6 次去重不告。
	for i := 0; i < 6; i++ {
		a.quoteReconnectBackoff(ctx)
	}

	mu.Lock()
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert (dedup), got %d: %v", len(alerts), alerts)
	}
	mu.Unlock()

	// 模拟成功帧 → quoteAlerted 重置
	a.quoteAlerted = false

	// 再次封顶 → 应再告 1 次
	for i := 0; i < 3; i++ { // idx 已到 6，每次都封顶
		a.quoteReconnectBackoff(ctx)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts after reset, got %d: %v", len(alerts), alerts)
	}
}

// contains 简单子串检查（避免 strings 导入冲突）。
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
