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
	"arb/internal/fact"
	"arb/internal/safego"

	"github.com/gorilla/websocket"
)

// okxPublicWSURL OKX 公共 WS 端点（spot+perp tickers 同连接）。
const okxPublicWSURL = "wss://ws.okx.com:8443/ws/v5/public"

// OKXWebSocket 订阅 OKX tickers 频道（spot + perp）。
type OKXWebSocket struct {
	cfg    Config
	dialer websocket.Dialer
	wsURL  string // 默认 okxPublicWSURL；测试覆写指向 fake server
	// D-507：时距字段化（默认=ws_keepalive.go 常量族；测试覆写缩小观察窗）。
	readIdle         time.Duration
	pingEvery        time.Duration
	reconnectBackoff time.Duration
	// emitFacts D-509：funding fact 发射口（FactEmitter 去重节流；nil=不 emit）。
	emitFacts func(ctx context.Context, fs []fact.Fact) error
}

// NewOKXWebSocket 创建 OKX WS 采集器。
func NewOKXWebSocket(cfg Config) *OKXWebSocket {
	d := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 10 * time.Second,
	}
	return &OKXWebSocket{cfg: cfg, dialer: d, wsURL: okxPublicWSURL,
		readIdle: quoteWSReadIdle, pingEvery: quoteWSPingEvery, reconnectBackoff: quoteWSReconnectBackoff}
}

// Run 连接 OKX WS 并持续推送报价到 publish 回调。阻塞至 ctx 取消。
func (o *OKXWebSocket) Run(ctx context.Context, publish func(bus.Quote)) {
	meter := newReconnectMeter(o.wsURL, o.cfg.AlertFn)
	for {
		if err := o.runOnce(ctx, publish); err != nil {
			meter.record()
			select {
			case <-ctx.Done():
				return
			case <-time.After(o.reconnectBackoff):
				log.Printf("[okx-ws] reconnect after error: %v", err)
			}
		}
	}
}

func (o *OKXWebSocket) runOnce(ctx context.Context, publish func(bus.Quote)) error {
	conn, _, err := o.dialer.DialContext(ctx, o.wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// 订阅 spot + perp tickers（D-369 S2：subscribed 集跟踪，ping 期差集增量订阅）
	subscribed := map[string]bool{}
	subscribeArgs := func(syms []string) []map[string]string {
		var args []map[string]string
		for _, sym := range syms {
			instID := sym + "-USDT"
			perpID := sym + "-USDT-SWAP"
			key := instID + "|" + perpID
			if subscribed[key] {
				continue
			}
			subscribed[key] = true
			args = append(args,
				map[string]string{"channel": "tickers", "instId": instID},
				map[string]string{"channel": "tickers", "instId": perpID},
			)
		}
		return args
	}
	sub := map[string]any{"op": "subscribe", "args": subscribeArgs(o.syms())}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	// D-507：读限保活收编——订阅成功后设一次+pong 续期（私有流先例形态）；
	// 读循环不再逐读设绝对读限（安静行情窗 pong 不触读循环会误断=重连风暴病灶）。
	wsKeepalive(conn, o.readIdle)

	// 启动 ping 保活 + 差集增量订阅（provider 符号集变化时；服务端拒则读报错走重连）
	done := make(chan struct{})
	safego.Go("okx_ws:ping", func() {
		ticker := time.NewTicker(o.pingEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
				if args := subscribeArgs(o.syms()); len(args) > 0 {
					_ = conn.WriteJSON(map[string]any{"op": "subscribe", "args": args})
				}
			case <-done:
				return
			}
		}
	})

	for {
		select {
		case <-ctx.Done():
			close(done)
			return nil
		default:
		}

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

// syms D-369 S2：okx WS 订阅符号集（env ∪ provider 动态集，连接期可增量）。
func (o *OKXWebSocket) syms() []string { return o.cfg.effectiveSymbols() }

// SetFactEmit 注入 fact 发射口（wiring 期；先于 Run）。
func (o *OKXWebSocket) SetFactEmit(fn func(ctx context.Context, fs []fact.Fact) error) {
	o.emitFacts = fn
}
