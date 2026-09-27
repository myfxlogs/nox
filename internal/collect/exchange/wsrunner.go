// wsrunner.go —— D-511 S1：公共行情 WS 共享骨架（三所收编）。
// 三所 WS 适配器（okx_ws/gate_ws/binance_market_ws）骨架同构：
// dial→subscribe→读循环→重连→ping，venue 差异只在钩子（URL/订阅帧/帧分发/增量补订）。
// 骨架行为一处钉死：读续期（任意帧或 pong 即续期，走 wsKeepalive 同件）、
// ping 经 Sender 单写者锁、退避重连重订阅、OnReconnect 计 reconnectMeter 风暴
// fail-loud（D-507 语义）、ctx 取消即刻退出。
// 安全契（D-450/R3）：骨架不自行取 httpx.WSDialer 默认 header——spec.Header==nil
// =显式无鉴权头直连，token 永不泄给原生 venue 域；dialer/header 由适配层全权供给。
package exchange

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"arb/internal/safego"

	"github.com/gorilla/websocket"
)

// Sender 单写者契约封装（mu 内写，防 ping goroutine 与订阅帧交叉）。
type Sender interface {
	Send(frame any) error
	SendRaw(messageType int, data []byte) error
}

// wsSender Sender 实现：mu 内嵌值（非指针），ping 与订阅帧共用同一把锁。
type wsSender struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (s *wsSender) Send(frame any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteJSON(frame)
}

func (s *wsSender) SendRaw(messageType int, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteMessage(messageType, data)
}

// WSSpec 公共行情 WS 规格（venue 差异收敛为钩子）。
type WSSpec struct {
	Name string // meter/log 标（如 "okx-ws"/"gate-ws:url"）
	URL  string // 完整 wss:// 路由（适配层已过 wsViaProxy）
	// URLs 有序候选地址表（cex-proxy 双端点 failover）：非空时代替 URL 字段逐序试拨——
	// 每次连接尝试都从首元素起（主端优先，回切无粘滞=主端恢复即自动回主）。
	URLs   []string
	Dialer *websocket.Dialer // 适配层供给（wsDialer/httpx.WSDialer）；nil=裸默认直连
	Header http.Header       // 显式握手鉴权头（binance=x-proxy-token；nil=显式无头）

	InitialSubs func() []any      // 首连+每重连订阅帧集（逐帧经 Sender.Send 发出）
	OnFrame     func(data []byte) // 数据帧分发（ack/无关帧由实现方自滤）
	OnConnect   func()            // 可选：每连接拨号成功后回调（差集表等每连接状态重置走此）
	OnAliveTick func(s Sender)    // 可选：每 ping tick 回调（okx/gate 差集补订走此）
	OnReconnect func()            // 可选：重连错误事件（reconnectMeter.record 走此）

	// 时距（零值=ws_keepalive.go 常量族；测试覆写缩小观察窗，对齐 D-507 字段化先例）。
	ReadIdle         time.Duration
	PingEvery        time.Duration
	ReconnectBackoff time.Duration
}

// RunWS 公共骨架入口：dial→OnConnect→InitialSubs→ping 保活→读循环→退避重连→阻塞至 ctx 取消。
// 双连接（gate spot+futures / binance perp+spot）由调用方启两个 goroutine 各传一 WSSpec。
func RunWS(ctx context.Context, spec WSSpec) {
	backoff := spec.ReconnectBackoff
	if backoff == 0 {
		backoff = quoteWSReconnectBackoff
	}
	for {
		if err := runWSOnce(ctx, spec); err != nil {
			if spec.OnReconnect != nil {
				spec.OnReconnect() // D-507 meter fail-loud（每连接一 meter，单 goroutine 持有）
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				log.Printf("[%s] reconnect after error: %v", spec.Name, err)
			}
		}
	}
}

// runWSOnce 单次连接：dial（显式 header）→OnConnect→逐帧发 InitialSubs→ping 保活→读循环。
func runWSOnce(ctx context.Context, spec WSSpec) error {
	dialer := spec.Dialer
	if dialer == nil {
		dialer = &websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 10 * time.Second}
	}
	urls := spec.URLs
	if len(urls) == 0 {
		urls = []string{spec.URL}
	}
	var conn *websocket.Conn
	var err error
	var lastURL string
	for _, u := range urls {
		lastURL = u
		conn, _, err = dialer.DialContext(ctx, u, spec.Header)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			break // ctx 取消——不再试下一候选
		}
		log.Printf("[%s] dial %s failed: %v（尝试下一候选）", spec.Name, u, err)
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", lastURL, err)
	}
	defer conn.Close()

	if spec.OnConnect != nil {
		spec.OnConnect()
	}

	// sender 持本连接唯一写锁——InitialSubs/ping/OnAliveTick 补订帧全经同一锁（单写者契约）。
	sender := &wsSender{conn: conn}
	if spec.InitialSubs != nil {
		for _, frame := range spec.InitialSubs() {
			if err := sender.Send(frame); err != nil {
				return fmt.Errorf("subscribe: %w", err)
			}
		}
	}

	readIdle := spec.ReadIdle
	if readIdle == 0 {
		readIdle = quoteWSReadIdle
	}
	// D-507：订阅成功后设一次+pong 续期（wsKeepalive 同件，读循环不设逐读绝对读限）。
	wsKeepalive(conn, readIdle)

	pingEvery := spec.PingEvery
	if pingEvery == 0 {
		pingEvery = quoteWSPingEvery
	}
	done := make(chan struct{})
	defer close(done)
	safego.Go(spec.Name+":ws-ping", func() {
		ticker := time.NewTicker(pingEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := sender.SendRaw(websocket.PingMessage, nil); err != nil {
					return
				}
				if spec.OnAliveTick != nil {
					spec.OnAliveTick(sender)
				}
			case <-done:
				return
			}
		}
	})

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(readIdle))
		if spec.OnFrame != nil {
			spec.OnFrame(data)
		}
	}
}
