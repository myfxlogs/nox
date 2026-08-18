// OKX WebSocket 行情采集（§3.3 后续迭代）。
// 连接 OKX 公共 WS，订阅 tickers 频道，实时推送 spot/perp bid/ask 到 QuoteBus。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"arb/internal/bus"

	"github.com/gorilla/websocket"
)

// OKXWebSocket 订阅 OKX tickers 频道（spot + perp）。
type OKXWebSocket struct {
	cfg    Config
	dialer websocket.Dialer
}

// NewOKXWebSocket 创建 OKX WS 采集器。
func NewOKXWebSocket(cfg Config) *OKXWebSocket {
	d := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 10 * time.Second,
	}
	return &OKXWebSocket{cfg: cfg, dialer: d}
}

// Run 连接 OKX WS 并持续推送报价到 publish 回调。阻塞至 ctx 取消。
func (o *OKXWebSocket) Run(ctx context.Context, publish func(bus.Quote)) {
	for {
		if err := o.runOnce(ctx, publish); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				log.Printf("[okx-ws] reconnect after error: %v", err)
			}
		}
	}
}

func (o *OKXWebSocket) runOnce(ctx context.Context, publish func(bus.Quote)) error {
	url := "wss://ws.okx.com:8443/ws/v5/public"
	conn, _, err := o.dialer.DialContext(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// 订阅 spot + perp tickers
	var args []map[string]string
	for _, sym := range o.cfg.Symbols {
		instID := sym + "-USDT"
		perpID := sym + "-USDT-SWAP"
		args = append(args,
			map[string]string{"channel": "tickers", "instId": instID},
			map[string]string{"channel": "tickers", "instId": perpID},
		)
	}
	sub := map[string]any{"op": "subscribe", "args": args}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	// 启动 ping 保活
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			close(done)
			return nil
		default:
		}

		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			close(done)
			return fmt.Errorf("read: %w", err)
		}
		o.handleMessage(data, publish)
	}
}

func (o *OKXWebSocket) handleMessage(data []byte, publish func(bus.Quote)) {
	var msg wsMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	if msg.Event == "subscribe" || msg.Event == "channel-conn-count" {
		return
	}
	if msg.Arg.Channel != "tickers" || len(msg.Data) == 0 {
		return
	}

	instID, _ := msg.Arg.InstID.(string)
	if instID == "" {
		return
	}
	venueSymbol := instID
	isPerp := strings.HasSuffix(instID, "-SWAP")
	venueType := "spot"
	if isPerp {
		venueType = "perp"
		instID = strings.TrimSuffix(instID, "-SWAP")
	}
	canonical := strings.ReplaceAll(instID, "-", "")

	item := msg.Data[0]
	bidS, _ := item["bidPx"].(string)
	askS, _ := item["askPx"].(string)
	tsS, _ := item["ts"].(string)

	var bid, ask float64
	fmt.Sscanf(bidS, "%f", &bid)
	fmt.Sscanf(askS, "%f", &ask)
	if bid == 0 || ask == 0 {
		return
	}

	ts := time.Now()
	if t, err := parseOKXTS(tsS); err == nil {
		ts = t
	}

	publish(bus.Quote{
		Symbol:      canonical,
		Venue:       VenueOKX,
		VenueSymbol: venueSymbol,
		Bid:         bid,
		Ask:         ask,
		Time:        ts,
		VenueType:   venueType,
	})
}

func parseOKXTS(s string) (time.Time, error) {
	var ms int64
	fmt.Sscanf(s, "%d", &ms)
	if ms == 0 {
		return time.Time{}, fmt.Errorf("bad ts")
	}
	return time.UnixMilli(ms), nil
}

type wsMsg struct {
	Event string `json:"event"`
	Arg   struct {
		Channel string `json:"channel"`
		InstID  any    `json:"instId"`
	} `json:"arg"`
	Data []map[string]any `json:"data"`
}
