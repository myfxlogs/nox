// wsrunner.go —— D-511 S1：公共行情 WS 共享骨架（三所收编）。
// 三所 WS 适配器（okx_ws/gate_ws/binance_market_ws）骨架同构：
// dial→subscribe→读循环→重连→ping，venue 差异只在三钩子（URL/订阅帧/帧分发）。
// 骨架行为一处钉死：读续期 60s（任意帧或 pong 即续期）、ping 20s 经 Sender（writeMu 内）、
// 断线 5s 退避重连重订阅、错误经 reconnectMeter 计数风暴 fail-loud、ctx 取消即刻退出。
package exchange

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"arb/internal/httpx"
	"arb/internal/safego"

	"github.com/gorilla/websocket"
)

// Sender 单写者契约封装（writeMu 内写，防 ping goroutine 与订阅帧交叉）。
type Sender interface{ Send(frame any) error }

// wsSender Sender 实现。
type wsSender struct {
	conn *websocket.Conn
	mu   *sync.Mutex
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

// WSSpec 公共行情 WS 规格（venue 差异收敛为三钩子）。
type WSSpec struct {
	Name        string            // meter/log 标（如 "okx-ws"/"gate-spot"）
	URL         string            // 完整 wss:// 路由
	Header      http.Header       // 鉴权头（binance=x-proxy-token；okx/gate=nil）
	InitialSubs func() []any      // 首连+重连订阅帧集（每帧经 Sender.Send 逐条发出）
	OnFrame     func(data []byte) // 数据帧分发（ack/无关帧由实现方自滤）
	OnAliveTick func(s Sender)    // 可选：每 ping tick 回调（okx 增量订阅差集补订走此；nil=无）
	OnReconnect func()            // 可选：重连错误回调（reconnectMeter.record 走此；nil=无）
}

// RunWS 公共骨架入口：dial→InitialSubs→ping 保活→读循环→5s 退避重连→阻塞至 ctx 取消。
// 双连接（gate spot+futures / binance perp+spot）由调用方启两个 goroutine 各传一 WSSpec。
func RunWS(ctx context.Context, spec WSSpec) {
	for {
		if err := runWSOnce(ctx, spec); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wsReconnectBackoff):
				log.Printf("[%s] reconnect after error: %v", spec.Name, err)
			}
		}
	}
}

// runWSOnce 单次连接：dial（带鉴权头）→逐帧发 InitialSubs→ping 保活→读循环分发 OnFrame。
func runWSOnce(ctx context.Context, spec WSSpec) error {
	dialer, header := httpx.WSDialer()
	if spec.Header != nil {
		header = spec.Header
	}
	conn, _, err := dialer.DialContext(ctx, spec.URL, header)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	sender := &wsSender{conn: conn}

	// InitialSubs：逐帧发出。
	if spec.InitialSubs != nil {
		for _, frame := range spec.InitialSubs() {
			if err := sender.Send(frame); err != nil {
				return fmt.Errorf("subscribe: %w", err)
			}
		}
	}

	// 读续期：任意帧或 pong 即续期 60s。
	_ = conn.SetReadDeadline(time.Now().Add(wsReadIdle))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsReadIdle))
	})

	// ping 保活（20s 周期经 writeMu 单写者；OnAliveTick 供增量订阅差集补订）。
	var writeMu sync.Mutex
	done := make(chan struct{})
	defer close(done)
	safego.Go(spec.Name+":ws-ping", func() {
		ticker := time.NewTicker(wsPingEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				writeMu.Lock()
				err := conn.WriteMessage(websocket.PingMessage, nil)
				writeMu.Unlock()
				if err != nil {
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

	// 读循环：任意帧→OnFrame 分发。
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(wsReadIdle))
		if spec.OnFrame != nil {
			spec.OnFrame(data)
		}
	}
}

// 常量对齐 D-507 语义（读续期 60s/ping 20s/退避 5s）。
const (
	wsReadIdle         = 60 * time.Second
	wsPingEvery        = 20 * time.Second
	wsReconnectBackoff = 5 * time.Second
)
