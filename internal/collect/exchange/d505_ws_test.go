// d505_ws_test.go —— D-507 保活收编红绿锚（编号沿用撞号后留痕：本票原占 D-505）。
// fake server 两变体（复用 d450 形态）：quiet=gorilla 默认自动回 pong 不推数据；
// dead=吞 ping 不回 pong 不推数据（SetPingHandler 返 nil 不写回）。
// 双向钉死：quiet 窗内零重连（pong 续期防误断）、dead 窗内判死重连（liveness 语义保）。
package exchange

import (
	"context"
	"net/http"
	"net/http/httptest"

	"arb/internal/bus"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// d507Server 计数 upgrade 的静默 WS server；dead=true 吞 ping 不回 pong。
func d507Server(t *testing.T, dead bool) (url string, upgrades *atomic.Int64) {
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
			conn.SetPingHandler(func(string) error { return nil }) // 吞 ping 不回 pong
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil { // 只排空订阅帧，永不推数据
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), n
}

const d507Observe = 1500 * time.Millisecond

func d507GateWS() *GateWebSocket {
	g := NewGateWebSocket(Config{})
	g.readIdle = 300 * time.Millisecond
	g.pingEvery = 100 * time.Millisecond
	g.reconnectBackoff = 50 * time.Millisecond
	return g
}

// TestD507_GateQuietFeedStaysAlive 安静行情窗（服务端不推数据但回 pong）：
// 观察窗内 upgrade 必须==1——S0 态（逐读绝对读限）下必红（300ms 超时重连≥2）。
func TestD507_GateQuietFeedStaysAlive(t *testing.T) {
	url, n := d507Server(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := d507GateWS()
	go g.runLoop(ctx, url, "spot.tickers", g.handleSpot, func(q bus.Quote) {})
	time.Sleep(d507Observe)
	cancel()
	if got := n.Load(); got != 1 {
		t.Fatalf("quiet feed 被误断重连: upgrades=%d want 1（pong 应续期读限）", got)
	}
}

// TestD507_GateDeadPeerStillReconnects 死对端（吞 ping 无 pong 无数据）：
// 读限判死 → 重连，观察窗内 upgrade ≥2（liveness 语义保留）。
func TestD507_GateDeadPeerStillReconnects(t *testing.T) {
	url, n := d507Server(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := d507GateWS()
	go g.runLoop(ctx, url, "spot.tickers", g.handleSpot, func(q bus.Quote) {})
	time.Sleep(d507Observe)
	cancel()
	if got := n.Load(); got < 2 {
		t.Fatalf("dead peer 未判死重连: upgrades=%d want ≥2", got)
	}
}

// TestD507_OkxQuietFeedStaysAlive okx 同 quiet 形态（S0 同红→S1 绿）。
func TestD507_OkxQuietFeedStaysAlive(t *testing.T) {
	url, n := d507Server(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := NewOKXWebSocket(Config{})
	o.wsURL = url
	o.readIdle = 300 * time.Millisecond
	o.pingEvery = 100 * time.Millisecond
	o.reconnectBackoff = 50 * time.Millisecond
	go o.Run(ctx, func(q bus.Quote) {})
	time.Sleep(d507Observe)
	cancel()
	if got := n.Load(); got != 1 {
		t.Fatalf("okx quiet feed 被误断重连: upgrades=%d want 1", got)
	}
}

// TestD507_NilAlertFnNoPanic AlertFn 缺省 + dead server 跑一轮无 panic（meter 静默跳过）。
func TestD507_NilAlertFnNoPanic(t *testing.T) {
	url, _ := d507Server(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := d507GateWS() // AlertFn 缺省 nil
	go g.runLoop(ctx, url, "spot.tickers", g.handleSpot, func(q bus.Quote) {})
	time.Sleep(d507Observe)
	cancel()
}
