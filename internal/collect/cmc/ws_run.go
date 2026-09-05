// CMC WebSocket runOnce + handleMessage（从 ws.go 拆出，保 ws.go ≤300 行）。
package cmc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/safego"

	"github.com/gorilla/websocket"
)

func (w *WsClient) runOnce(ctx context.Context) error {
	jwt, err := w.auth.JWT(ctx)
	if err != nil {
		return fmt.Errorf("jwt: %w", err)
	}
	wsURL := strings.Replace(w.wsBase, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	// 1. auth（等 ack {"auth":{"status":"ok"}}，超时 10s 失败重连）
	authMsg, _ := json.Marshal(map[string]any{"auth": map[string]any{"token": jwt}})
	if err := w.writeMsg(conn, authMsg); err != nil {
		return fmt.Errorf("auth write: %w", err)
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, ackData, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("auth ack read: %w", err)
	}
	var ackEnv wsEnvelope
	if err := json.Unmarshal(ackData, &ackEnv); err != nil || ackEnv.Auth == nil {
		return fmt.Errorf("auth ack parse: %w (first frame not auth ack)", err)
	}
	if ackEnv.Auth.Status != "ok" {
		return fmt.Errorf("auth ack status=%s", ackEnv.Auth.Status)
	}
	// 2. D-225 v2.1：取消 REST 播种——type7 首帧全量快照=种子（F3）；5s 无首帧 → REST 兜底一次。
	w.seedDone.Store(false)
	// 3. subscribe 5/6/7
	for _, t := range []int{5, 6, 7} {
		msg, _ := json.Marshal(map[string]any{
			"subscribe": map[string]any{"subscriptionId": fmt.Sprintf("%d", w.accountID), "type": t},
		})
		if err := w.writeMsg(conn, msg); err != nil {
			return fmt.Errorf("subscribe type %d: %w", t, err)
		}
	}
	log.Printf("[cmc-ws] connected+subscribed account=%d", w.accountID)
	// 4. heartbeat goroutine
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(wsHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				hb, _ := json.Marshal(map[string]any{"heartbeat": map[string]any{}})
				if err := w.writeMsg(conn, hb); err != nil {
					return
				}
			}
		}
	}()
	// 5. D-225 v2.1：5s 首帧兜底定时器
	safego.Go("cmc-ws:seedFallback", func() {
		select {
		case <-done:
			return
		case <-time.After(wsSeedFallbackDelay):
			if !w.seedDone.Load() {
				log.Printf("[cmc-ws] no type7 snapshot in 5s, REST seed fallback")
				w.restSeedPositions(ctx, jwt)
			}
		}
	})
	// 6. 价格心跳定时器（4min）
	w.startPriceHeartbeat(ctx, done)
	// 7. read loop
	w.healthy.Store(true)
	w.markEvent6()
	for {
		if ctx.Err() != nil {
			return nil
		}
		conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		_, data, err := conn.ReadMessage()
		if err != nil {
			w.healthy.Store(false)
			return fmt.Errorf("read: %w", err)
		}
		w.handleMessage(ctx, data)
	}
}

func (w *WsClient) handleMessage(ctx context.Context, data []byte) {
	var env wsEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}
	if env.Auth != nil {
		return // auth ack 由 runOnce 处理
	}
	if env.Init != nil {
		log.Printf("[cmc-ws] init: heartbeatInterval=%dms heartbeatTimeout=%dms subscriptions=%d",
			env.Init.HeartbeatInterval, env.Init.HeartbeatTimeout, env.Init.SubscriptionsCount)
		return
	}
	if env.Subscribe != nil {
		if env.Subscribe.Status != "ok" {
			log.Printf("[cmc-ws] subscribe ack status=%s (非 ok)", env.Subscribe.Status)
		}
		return
	}
	if env.Heartbeat != nil {
		if env.Heartbeat.Status != "ok" {
			log.Printf("[cmc-ws] heartbeat ack status=%s (非 ok)", env.Heartbeat.Status)
		}
		return
	}
	if env.Msg == nil {
		return
	}
	msg := env.Msg
	switch msg.Type {
	case 5:
		w.handleType5(ctx, msg.Data)
	case 6:
		w.handleType6(ctx, msg.Data)
		w.markEvent6() // D-225 v2.1：守卫只看 type6
	case 7:
		w.handleType7(ctx, msg)
	default:
		return
	}
}
