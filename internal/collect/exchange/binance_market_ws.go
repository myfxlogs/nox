// binance_market_ws.go —— D-505 S1：Binance 公开报价 WS 流化（替代 REST 轮询主路径）。
// 双连接：perp wss://{host}/ws、spot wss://{host}/spotws/ws（host=cfg.BinanceBaseURL 的
// scheme+host，同 BinanceUserWS.WSURL 取法——REST 路径段不进 WS URL）。
// 单连接多流：连上即发 SUBSCRIBE ["{sym}usdt@bookTicker",…]（流名小写），等 ack {result:null}。
// 双 payload 形制（D-505 决策席实测钉死）：perp 带 e/T/E 包裹、spot 裸 u/s/b/B/a/A——
// 统一取 s/b/B/a/A；时间戳取帧内 T（perp）否则本地 now。
// 鉴权：x-proxy-token 握口头必带（无→HTTP 403；httpx.WSDialer 同源注入）。
// 发布键形与原 REST poll 逐字一致（venue=binance、VenueType=perp/spot、Symbol=pair；D-508 S14 起单轨 WS）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
	"arb/internal/httpx"
	"arb/internal/safego"

	"github.com/gorilla/websocket"
)

// BinanceMarketWS Binance 公开行情 WS（bookTicker 双连接订阅）。
type BinanceMarketWS struct {
	cfg     Config
	perpURL string
	spotURL string
	dialer  *websocket.Dialer
	header  http.Header
	now     func() time.Time
	// writeMu gorilla conn 单写者契约：ping goroutine 与 SUBSCRIBE 写共用 conn。
	writeMu sync.Mutex
	// emitFacts D-509：funding fact 发射口（FactEmitter 去重节流；nil=不 emit）。
	emitFacts func(ctx context.Context, fs []fact.Fact) error
	// intervals D-509 S2：funding 周期缓存（fundingInfo 真值；nil=回退 3/天）。
	intervals *FundingIntervalCache
}

// NewBinanceMarketWS 创建采集器（host 取 cfg.BinanceBaseURL scheme+host）。
func NewBinanceMarketWS(cfg Config) *BinanceMarketWS {
	u, err := url.Parse(cfg.BinanceBaseURL)
	if err != nil {
		log.Printf("[binance-mkt-ws] parse baseURL %q: %v（WS 禁用，REST 兜底）", cfg.BinanceBaseURL, err)
		return nil
	}
	scheme := "wss"
	if u.Scheme == "http" {
		scheme = "ws"
	}
	d, h := httpx.WSDialer()
	return &BinanceMarketWS{
		cfg:     cfg,
		perpURL: scheme + "://" + u.Host + "/ws",
		spotURL: scheme + "://" + u.Host + "/spotws/ws",
		dialer:  d, header: h, now: time.Now,
	}
}

// Run 双连接并行（perp+spot），阻塞至 ctx 取消（mirror gate_ws.Run）。
func (b *BinanceMarketWS) Run(ctx context.Context, publish func(bus.Quote)) {
	if b == nil {
		return // baseURL 解析失败静默（REST 兜底在跑）
	}
	var wg sync.WaitGroup
	wg.Add(2)
	safego.Go("binance_market_ws:perp", func() {
		defer wg.Done()
		b.runLoop(ctx, b.perpURL, "perp", publish)
	})
	safego.Go("binance_market_ws:spot", func() {
		defer wg.Done()
		b.runLoop(ctx, b.spotURL, "spot", publish)
	})
	wg.Wait()
}

// runLoop 单连接重连循环（5s 退避，mirror gate_ws.runLoop）。
func (b *BinanceMarketWS) runLoop(ctx context.Context, url, venueType string, publish func(bus.Quote)) {
	meter := newReconnectMeter(venueType+":"+url, b.cfg.AlertFn) // D-507 S2 扩：同款风暴告警
	for {
		if err := b.runOnce(ctx, url, venueType, publish); err != nil {
			meter.record()
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				log.Printf("[binance-mkt-ws] %s reconnect after error: %v", venueType, err)
			}
		}
	}
}

// runOnce 单次连接：dial（带 token 头）→ SUBSCRIBE 全量符号 → 等 ack → 读循环分发。
func (b *BinanceMarketWS) runOnce(ctx context.Context, url, venueType string, publish func(bus.Quote)) error {
	conn, _, err := b.dialer.DialContext(ctx, url, b.header)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// 符号源与原 ticker 同源（effectiveSymbols：env ∪ SymProvider
	// 动态 watchlist）；watchlist 新增符号随下次重连订阅生效，60s REST 兜底补窗。
	syms := b.cfg.effectiveSymbols()
	params := make([]string, 0, len(syms))
	for _, sym := range syms {
		params = append(params, strings.ToLower(sym+"usdt")+"@bookTicker")
	}
	if err := conn.WriteJSON(map[string]any{
		"method": "SUBSCRIBE", "params": params, "id": 1,
	}); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	_ = conn.SetReadDeadline(b.now().Add(binanceMktReadIdle))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(b.now().Add(binanceMktReadIdle))
	})
	done := make(chan struct{})
	defer close(done)
	safego.Go("binance_market_ws:ping", func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				b.writeMu.Lock()
				err := conn.WriteMessage(websocket.PingMessage, nil)
				b.writeMu.Unlock()
				if err != nil {
					return
				}
			case <-done:
				return
			}
		}
	})

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		_ = conn.SetReadDeadline(b.now().Add(binanceMktReadIdle))
		if isSubscribeAck(data) {
			continue // {result:null,id:1} ack；{result:…,error} 已在 isSubscribeAck 记日志
		}
		b.handleBookTicker(data, venueType, publish)
	}
}

// bookTickerFrame 双形制统一投影（perp 带 e/T/E 包裹、spot 裸 u/s/b/B/a/A——取交集字段）。
// bookTickerFrame 手工取键：载荷同时含 b(价)/B(量)、a(价)/A(量)——Go json tag
// 大小写不敏感回退会令 "B" 覆盖 "b" 绑定（实测 bid=2.5=量），禁 struct tag 直解。
type bookTickerFrame struct {
	S  string
	B  string // bid 价（小写 b）
	A  string // ask 价（小写 a）
	BQ string // bid 量（大写 B）
	AQ string // ask 量（大写 A）
	T  int64  // perp 有；spot 零值→本地 now
}

func parseBookTicker(data []byte) (bookTickerFrame, bool) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return bookTickerFrame{}, false
	}
	f := bookTickerFrame{}
	if v, ok := m["s"].(string); ok {
		f.S = v
	}
	if v, ok := m["b"].(string); ok {
		f.B = v
	}
	if v, ok := m["a"].(string); ok {
		f.A = v
	}
	if v, ok := m["B"].(string); ok {
		f.BQ = v
	}
	if v, ok := m["A"].(string); ok {
		f.AQ = v
	}
	if v, ok := m["T"].(float64); ok {
		f.T = int64(v)
	}
	return f, f.S != "" && f.B != "" && f.A != ""
}

func (b *BinanceMarketWS) handleBookTicker(data []byte, venueType string, publish func(bus.Quote)) {
	f, ok := parseBookTicker(data)
	if !ok {
		return // 订阅 ack/错误帧/无关帧静默（ack 已在 isSubscribeAck 分流）
	}
	var bidF, askF float64
	fmt.Sscanf(f.B, "%f", &bidF)
	fmt.Sscanf(f.A, "%f", &askF)
	ts := b.now()
	if f.T > 0 {
		ts = time.UnixMilli(f.T)
	}
	publish(bus.Quote{
		Symbol: f.S, Venue: VenueBinance, VenueType: venueType, VenueSymbol: f.S,
		Bid: bidF, Ask: askF, Time: ts,
	})
}

// isSubscribeAck 订阅 ack/错误帧判定：{result:…,id} 形（数据帧无 result 字段）。
func isSubscribeAck(data []byte) bool {
	var probe struct {
		Result any   `json:"result"`
		Error  any   `json:"error"`
		ID     int64 `json:"id"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	if probe.Error != nil {
		log.Printf("[binance-mkt-ws] subscribe error frame: %s", string(data))
		return true
	}
	return probe.ID != 0
}

// binanceMktReadIdle 读空闲判定窗（pong/push 均续期；超时→runLoop 重连）。
const binanceMktReadIdle = 60 * time.Second

// SetFactEmit 注入 fact 发射口（wiring 期；先于 Run）。
func (b *BinanceMarketWS) SetFactEmit(fn func(ctx context.Context, fs []fact.Fact) error) {
	b.emitFacts = fn
}

// SetFundingIntervals 注入 funding 周期缓存（与 BinanceFunding 共用 D-301a boot 真值）。
func (b *BinanceMarketWS) SetFundingIntervals(c *FundingIntervalCache) { b.intervals = c }
