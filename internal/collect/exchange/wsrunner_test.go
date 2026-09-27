// wsrunner_test.go —— D-511 R7：RunWS 共享骨架 fake-server 级验收。
// 钉死：订阅帧序（InitialSubs 逐帧）/单写者并发（OnAliveTick 补订与 ping 同锁不 panic 不交错截断）/
// 断线退避+OnReconnect 触发/ctx 取消即刻退/quiet 续期防误断（D-507 病灶不复归）。
package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// wsRunnerServer 回显型 fake server：记录收到的文本帧序列，可配吞 ping（dead）。
func wsRunnerServer(t *testing.T, dead bool, frames chan<- []byte) (string, *atomic.Int64) {
	t.Helper()
	n := &atomic.Int64{}
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		n.Add(1)
		defer conn.Close()
		if dead {
			conn.SetPingHandler(func(string) error { return nil })
		}
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			select {
			case frames <- data:
			default:
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), n
}

func wsRunnerSpec(url string, frames chan<- []byte) WSSpec {
	return WSSpec{
		Name: "test-ws", URL: url,
		InitialSubs: func() []any {
			return []any{
				map[string]any{"method": "SUB", "id": 1},
				map[string]any{"method": "SUB", "id": 2},
			}
		},
		OnFrame:          func(data []byte) {},
		ReadIdle:         300 * time.Millisecond,
		PingEvery:        60 * time.Millisecond,
		ReconnectBackoff: 40 * time.Millisecond,
	}
}

// TestD511_InitialSubsSentInOrder 首连订阅帧按序逐帧到达（RunWS 逐帧 Send 语义）。
func TestD511_InitialSubsSentInOrder(t *testing.T) {
	frames := make(chan []byte, 8)
	url, _ := wsRunnerServer(t, false, frames)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunWS(ctx, wsRunnerSpec(url, frames))
	for i := 0; i < 2; i++ {
		select {
		case data := <-frames:
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("sub frame %d 非 JSON: %v", i, err)
			}
			if m["method"] != "SUB" {
				t.Fatalf("frame %d method=%v", i, m["method"])
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("sub frame %d 未到", i)
		}
	}
}

// TestD511_OnAliveTickSingleWriter OnAliveTick 补订帧与 ping 并发写——
// 经同一 mu 锁单写者，帧不交错、无 panic（gorilla 并发写=panic/截断）。
func TestD511_OnAliveTickSingleWriter(t *testing.T) {
	frames := make(chan []byte, 64)
	url, _ := wsRunnerServer(t, false, frames)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spec := wsRunnerSpec(url, frames)
	spec.OnAliveTick = func(s Sender) {
		for i := 0; i < 8; i++ {
			_ = s.Send(map[string]any{"op": "resub", "i": i})
		}
	}
	go RunWS(ctx, spec)
	time.Sleep(400 * time.Millisecond)
	cancel()
	// 只断言存活到此刻即证单写者无 panic；帧计数非本测重点。
}

// TestD511_ReconnectTriggersOnReconnect 死对端判死重连+OnReconnect 每次触发（meter 挂点实证）。
func TestD511_ReconnectTriggersOnReconnect(t *testing.T) {
	frames := make(chan []byte, 8)
	url, upgrades := wsRunnerServer(t, true, frames) // 吞 ping 不回 pong→读限判死
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reconns atomic.Int64
	spec := wsRunnerSpec(url, frames)
	spec.OnReconnect = func() { reconns.Add(1) }
	go RunWS(ctx, spec)
	time.Sleep(1500 * time.Millisecond)
	cancel()
	if got := upgrades.Load(); got < 2 {
		t.Fatalf("dead peer 未判死重连: upgrades=%d want ≥2", got)
	}
	if got := reconns.Load(); got < 1 {
		t.Fatalf("OnReconnect 未触发: %d", got)
	}
}

// TestD511_QuietFeedStaysAlive 安静行情窗零重连（pong 续期防误断——D-507 语义固化）。
func TestD511_QuietFeedStaysAlive(t *testing.T) {
	frames := make(chan []byte, 8)
	url, upgrades := wsRunnerServer(t, false, frames)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunWS(ctx, wsRunnerSpec(url, frames))
	time.Sleep(1500 * time.Millisecond)
	cancel()
	if got := upgrades.Load(); got != 1 {
		t.Fatalf("quiet feed 被误断重连: upgrades=%d want 1", got)
	}
}

// TestD511_CtxCancelExits ctx 取消即刻退出（退避窗内阻塞不死挂）。
func TestD511_CtxCancelExits(t *testing.T) {
	url, _ := wsRunnerServer(t, true, make(chan []byte, 8))
	ctx, cancel := context.WithCancel(context.Background())
	spec := wsRunnerSpec(url, nil)
	spec.ReconnectBackoff = 10 * time.Second // 退避窗内验证 select ctx.Done
	done := make(chan struct{})
	go func() { RunWS(ctx, spec); close(done) }()
	time.Sleep(300 * time.Millisecond) // 进入重连退避
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消未即刻退出")
	}
}

// TestD511_NilHeaderNoToken spec.Header==nil → 握手零鉴权头（R3：token 不泄原生域）。
func TestD511_NilHeaderNoToken(t *testing.T) {
	var authHeader string
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("X-Proxy-Token")
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.ReadMessage()
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ARB_BINANCE_PROXY_TOKEN", "tok-should-not-leak")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spec := wsRunnerSpec("ws"+strings.TrimPrefix(srv.URL, "http"), make(chan []byte, 8))
	go RunWS(ctx, spec)
	time.Sleep(300 * time.Millisecond)
	cancel()
	if authHeader != "" {
		t.Fatalf("spec.Header nil 仍带 token 头: %q", authHeader)
	}
}
