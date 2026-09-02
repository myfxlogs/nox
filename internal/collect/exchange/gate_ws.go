// Gate.io WebSocket 行情采集（§3.3 后续迭代）。
// spot + futures 各一条独立连接（URL 不同），订阅 tickers 频道实时推送 bid/ask 到 QuoteBus。
// 与 okx_ws.go 结构对齐：Run 循环 + 断线重连 + ping 保活。
//
// Binance 不做 WS：直连被地理封锁（451/403），且 CF/Render 代理不做 WebSocket 升级
// （实测 Upgrade 请求返回 200 非 101），故 Binance 保持 REST 轮询（pollBinanceQuotes）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/safego"

	"github.com/gorilla/websocket"
)

// Gate 公共 WS 端点。futures 走独立端点（USDT 本位合约），spot 走主端点。
const (
	gateSpotWSURL    = "wss://api.gateio.ws/ws/v4/"
	gateFuturesWSURL = "wss://fx-ws.gateio.ws/v4/ws/usdt"
)

// GateWebSocket 订阅 Gate spot + futures tickers。
type GateWebSocket struct {
	cfg    Config
	dialer websocket.Dialer
}

// NewGateWebSocket 创建 Gate WS 采集器。
func NewGateWebSocket(cfg Config) *GateWebSocket {
	d := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 10 * time.Second,
	}
	return &GateWebSocket{cfg: cfg, dialer: d}
}

// Run 连接 Gate spot + futures WS 并持续推送报价到 publish。阻塞至 ctx 取消。
func (g *GateWebSocket) Run(ctx context.Context, publish func(bus.Quote)) {
	var wg sync.WaitGroup
	wg.Add(2)
	safego.Go("gate_ws:spot-loop", func() {
		defer wg.Done()
		g.runLoop(ctx, gateSpotWSURL, g.subscribeSpot, g.handleSpot, publish)
	})
	safego.Go("gate_ws:futures-loop", func() {
		defer wg.Done()
		g.runLoop(ctx, gateFuturesWSURL, g.subscribeFutures, g.handleFutures, publish)
	})
	wg.Wait()
}

// subscribePayload 构造 Gate 订阅请求体（须带 time=unix 秒）。
func subscribePayload(channel string, payload []string) map[string]any {
	return map[string]any{
		"time":    time.Now().Unix(),
		"channel": channel,
		"event":   "subscribe",
		"payload": payload,
	}
}

// spotSymbols 返回 spot 交易对（BTC_USDT）。
func (g *GateWebSocket) spotSymbols() []string {
	out := make([]string, len(g.cfg.Symbols))
	for i, s := range g.cfg.Symbols {
		out[i] = s + "_USDT"
	}
	return out
}

func (g *GateWebSocket) subscribeSpot(c *websocket.Conn) error {
	return c.WriteJSON(subscribePayload("spot.tickers", g.spotSymbols()))
}

func (g *GateWebSocket) subscribeFutures(c *websocket.Conn) error {
	// futures 合约名与 spot 交易对同名（BTC_USDT）
	return c.WriteJSON(subscribePayload("futures.tickers", g.spotSymbols()))
}

// runLoop 单连接重连循环。
func (g *GateWebSocket) runLoop(ctx context.Context, url string, subscribe func(*websocket.Conn) error, handle func([]byte, func(bus.Quote)), publish func(bus.Quote)) {
	for {
		if err := g.runOnce(ctx, url, subscribe, handle, publish); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				log.Printf("[gate-ws] %s reconnect after error: %v", url, err)
			}
		}
	}
}

// runOnce 单次连接：dial → subscribe → ping 保活 → 读循环。
func (g *GateWebSocket) runOnce(ctx context.Context, url string, subscribe func(*websocket.Conn) error, handle func([]byte, func(bus.Quote)), publish func(bus.Quote)) error {
	conn, _, err := g.dialer.DialContext(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	if err := subscribe(conn); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	done := make(chan struct{})
	safego.Go("gate_ws:ping", func() {
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
	})

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
		handle(data, publish)
	}
}

// gateSpotUpdate spot.tickers 推送：result 为单对象，含 best bid/ask。
type gateSpotUpdate struct {
	Event  string `json:"event"`
	Result struct {
		CurrencyPair string `json:"currency_pair"`
		HighestBid   string `json:"highest_bid"`
		LowestAsk    string `json:"lowest_ask"`
	} `json:"result"`
}

// handleSpot 解析 spot.tickers 更新并发布报价。
func (g *GateWebSocket) handleSpot(data []byte, publish func(bus.Quote)) {
	var msg gateSpotUpdate
	if err := json.Unmarshal(data, &msg); err != nil || msg.Event != "update" {
		return
	}
	pair := msg.Result.CurrencyPair
	if pair == "" {
		return
	}
	var bid, ask float64
	fmt.Sscanf(msg.Result.HighestBid, "%f", &bid)
	fmt.Sscanf(msg.Result.LowestAsk, "%f", &ask)
	if bid == 0 || ask == 0 {
		return
	}
	publish(bus.Quote{
		Symbol:      canonicalFromGate(pair),
		Venue:       VenueGate,
		VenueType:   "spot",
		VenueSymbol: pair,
		Bid:         bid,
		Ask:         ask,
		Time:        time.Now(),
	})
}

// gateFuturesUpdate futures.tickers 推送：result 为数组，无 bid/ask，用 mark_price 近似。
type gateFuturesUpdate struct {
	Event  string `json:"event"`
	Result []struct {
		Contract  string `json:"contract"`
		MarkPrice string `json:"mark_price"`
		T         int64  `json:"t"` // unix ms
	} `json:"result"`
}

// handleFutures 解析 futures.tickers 更新并发布报价。
func (g *GateWebSocket) handleFutures(data []byte, publish func(bus.Quote)) {
	var msg gateFuturesUpdate
	if err := json.Unmarshal(data, &msg); err != nil || msg.Event != "update" {
		return
	}
	for _, item := range msg.Result {
		if item.Contract == "" {
			continue
		}
		var mark float64
		fmt.Sscanf(item.MarkPrice, "%f", &mark)
		if mark == 0 {
			continue
		}
		ts := time.Now()
		if item.T > 0 {
			ts = time.UnixMilli(item.T)
		}
		publish(bus.Quote{
			Symbol:      canonicalFromGate(item.Contract),
			Venue:       VenueGate,
			VenueType:   "perp",
			VenueSymbol: item.Contract,
			Bid:         mark,
			Ask:         mark,
			Time:        ts,
		})
	}
}

// canonicalFromGate 将 Gate 交易对/合约名（BTC_USDT）映射为 canonical（BTCUSDT）。
func canonicalFromGate(pair string) string {
	return strings.ReplaceAll(pair, "_", "")
}
