// binance_user_ws_spot.go —— D-459：Binance spot 用户数据流 ws-api 支路。
// REST userDataStream 端点 410 Gone 已下线（D-450 取证）；ws-api 实证（D-459 S0，
// 外部代理机 192.229.85.35 实测）：
//   - session.logon 拒 HMAC key（-2028，仅收 Ed25519）→ 不走 logon/subscribe
//   - userDataStream.start params={apiKey}（免签名）→ 200 + 60 位 listenKey
//   - userDataStream.ping/stop params={apiKey,listenKey} → 200
//   - 事件仍推 stream.binance.com/ws/<key>（既有 /spotws 代理路由），形制=经典
//     spot 用户流（executionReport/outboundAccountPosition/balanceUpdate）
//
// 拓扑：ws-api 控制连接（/wsapi → ws-api.binance.com/ws-api/v3）管生命周期，
// 流连接（/spotws/ws/<key>）收事件；60min 不用 ping 续租则 listenKey 失效——
// 续租失败/读断 → 关流重连重取 key（同 perp listenKey 语义，无 session 层）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"time"

	"arb/internal/safego"

	"github.com/gorilla/websocket"
)

// wsapiURL ws-api 控制端点（经代理 /wsapi 前缀 strip → ws-api.binance.com/ws-api/v3）。
func (b *BinanceUserWS) wsapiURL() (string, error) {
	u, err := url.Parse(b.baseURL)
	if err != nil {
		return "", fmt.Errorf("parse baseURL: %w", err)
	}
	scheme := "wss"
	if u.Scheme == "http" {
		scheme = "ws"
	}
	return fmt.Sprintf("%s://%s/wsapi/ws-api/v3", scheme, u.Host), nil
}

// wsapiCall 控制连接单次请求-响应（start/ping 共用形制：params 只放 apiKey+可选 listenKey）。
func (b *BinanceUserWS) wsapiCall(conn *websocket.Conn, method string, params map[string]any) (json.RawMessage, error) {
	if err := conn.WriteJSON(map[string]any{"id": method, "method": method, "params": params}); err != nil {
		return nil, err
	}
	_ = conn.SetReadDeadline(b.now().Add(15 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	var resp struct {
		Status int             `json:"status"`
		Result json.RawMessage `json:"result"`
		Error  struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("ws-api resp parse: %w", err)
	}
	if resp.Status != 200 {
		return nil, fmt.Errorf("ws-api %s: status=%d code=%d: %s", method, resp.Status, resp.Error.Code, resp.Error.Msg)
	}
	return resp.Result, nil
}

// runOnceSpot ws-api start 取 listenKey → 拨流连接收事件 → ping 续租 goroutine。
func (b *BinanceUserWS) runOnceSpot(ctx context.Context) error {
	wsapi, err := b.wsapiURL()
	if err != nil {
		return err
	}
	ctrl, err := b.dial(ctx, wsapi)
	if err != nil {
		return fmt.Errorf("dial ws-api: %w", err)
	}
	defer ctrl.Close()
	res, err := b.wsapiCall(ctrl, "userDataStream.start", map[string]any{"apiKey": b.apiKey})
	if err != nil {
		return fmt.Errorf("userDataStream.start: %w", err)
	}
	var r struct {
		ListenKey string `json:"listenKey"`
	}
	if err := json.Unmarshal(res, &r); err != nil || r.ListenKey == "" {
		return fmt.Errorf("listenKey response invalid: %s", truncateStr(string(res), 200))
	}
	wsURL, err := b.WSURL(r.ListenKey)
	if err != nil {
		return err
	}
	conn, err := b.dial(ctx, wsURL)
	if err != nil {
		return fmt.Errorf("dial stream: %w", err)
	}
	defer conn.Close()
	log.Printf("[binance-user-ws] spot connected via ws-api (venueKey=%s)", b.venueKey)

	_ = conn.SetReadDeadline(b.now().Add(binanceUserReadIdle))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(b.now().Add(binanceUserReadIdle))
	})

	// 续租 goroutine：renewEvery 一次 ws-api ping；失败 → 关流连接触发 runOnce 返回重取。
	done := make(chan struct{})
	defer close(done)
	safego.Go("binance_user_ws:spot_keepalive", func() {
		ticker := time.NewTicker(b.renewEvery)
		defer ticker.Stop()
		ping := time.NewTicker(20 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := b.wsapiCall(ctrl, "userDataStream.ping", map[string]any{
					"apiKey": b.apiKey, "listenKey": r.ListenKey,
				}); err != nil {
					log.Printf("[binance-user-ws] spot ws-api ping failed: %v (reconnecting for fresh key)", err)
					_ = conn.Close()
					return
				}
			case <-ping.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
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
		if b.handleFrame(ctx, data) {
			return fmt.Errorf("listenKeyExpired")
		}
	}
}
