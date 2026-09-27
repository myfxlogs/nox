// Gate.io WebSocket 行情采集（§3.3 后续迭代）。
// spot + futures 各一条独立连接（URL 不同），订阅 tickers 频道实时推送 bid/ask 到 QuoteBus；
// 与 okx_ws.go 结构对齐（Run 循环+断线重连+ping 保活）。funding 三件套亦走本帧（D-509 S3）。
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
	"arb/internal/fact"
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
	cfg         Config
	dialer      *websocket.Dialer
	header      http.Header // 代理模式=X-Proxy-Token 握口头；直连=nil
	spotURLs    []string    // 有序候选（cex-proxy 双端点 failover；[0]=主端）
	futuresURLs []string    // 同上
	// D-507：时距字段化（默认=ws_keepalive.go 常量族；测试覆写缩小观察窗）。
	readIdle         time.Duration
	pingEvery        time.Duration
	reconnectBackoff time.Duration
	// emitFacts D-509：funding fact 发射口（FactEmitter 去重节流；nil=不 emit）。
	emitFacts func(ctx context.Context, fs []fact.Fact) error
}

// NewGateWebSocket 创建 Gate WS 采集器（ARB_CEX_WS_BASE 置位→经 cex-proxy /gatespotws 与 /v4/ws 前缀）。
func NewGateWebSocket(cfg Config) *GateWebSocket {
	proxied := cexWSBase() != ""
	d, h := wsDialer(proxied)
	return &GateWebSocket{cfg: cfg, dialer: d, header: h,
		spotURLs:    wsViaProxyURLs("/gatespotws/ws/v4/", gateSpotWSURL),
		futuresURLs: wsViaProxyURLs("/v4/ws/usdt", gateFuturesWSURL),
		readIdle:    quoteWSReadIdle, pingEvery: quoteWSPingEvery, reconnectBackoff: quoteWSReconnectBackoff}
}

// Run 连接 Gate spot + futures WS 并持续推送报价到 publish。阻塞至 ctx 取消。
func (g *GateWebSocket) Run(ctx context.Context, publish func(bus.Quote)) {
	var wg sync.WaitGroup
	wg.Add(2)
	safego.Go("gate_ws:spot-loop", func() {
		defer wg.Done()
		g.runLoop(ctx, g.spotURLs, "spot.tickers", g.handleSpot, publish)
	})
	safego.Go("gate_ws:futures-loop", func() {
		defer wg.Done()
		g.runLoop(ctx, g.futuresURLs, "futures.tickers", g.handleFutures, publish)
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

// subscribeChannel 写订阅帧（全量或差集 pairs），经 Sender 单写者锁。
func (g *GateWebSocket) subscribeChannel(s Sender, channel string, pairs []string) error {
	return s.Send(subscribePayload(channel, pairs))
}

// runLoop 单连接重连循环——D-511 S3：骨架收编 RunWS（dial/保活/读循环/退避一处钉死），
// 本函数只装配协议钩子（URL=构造期 wsViaProxy 过的值、header=g.header 显式供给——R4/R3）。
func (g *GateWebSocket) runLoop(ctx context.Context, urls []string, channel string, handle func(context.Context, []byte, func(bus.Quote)), publish func(bus.Quote)) {
	meter := newReconnectMeter(urls[0], g.cfg.AlertFn)
	// subscribed 差集表为每连接状态——OnConnect 每拨号重置（重连全量重订阅语义不变）。
	var subscribed map[string]bool
	RunWS(ctx, WSSpec{
		Name: "gate-ws:" + urls[0], URLs: urls, Dialer: g.dialer, Header: g.header,
		ReadIdle: g.readIdle, PingEvery: g.pingEvery, ReconnectBackoff: g.reconnectBackoff,
		OnConnect: func() { subscribed = map[string]bool{} },
		InitialSubs: func() []any {
			pairs := pairsD369(g.syms(), "_USDT")
			for _, p := range pairs {
				subscribed[p] = true
			}
			return []any{subscribePayload(channel, pairs)}
		},
		// D-369 S2：provider 符号集变化 → ping tick 差集补订（新符号 only；服务端拒则读报错走重连）
		OnAliveTick: func(s Sender) {
			var newPairs []string
			for _, p := range pairsD369(g.syms(), "_USDT") {
				if !subscribed[p] {
					newPairs = append(newPairs, p)
				}
			}
			if len(newPairs) > 0 && g.subscribeChannel(s, channel, newPairs) == nil {
				for _, p := range newPairs {
					subscribed[p] = true
				}
			}
		},
		OnFrame:     func(data []byte) { handle(ctx, data, publish) },
		OnReconnect: meter.record,
	})
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
func (g *GateWebSocket) handleSpot(_ context.Context, data []byte, publish func(bus.Quote)) {
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
// D-509 S3 实测字段（fx-ws 公共帧）：funding_rate/funding_next_apply(unix s)/
// funding_interval(s) 原生在帧——funding 三件套自帧内取（rate+next_apply），interval
// fact 归日轮 gate_contract_meta（S5 起唯一职责=元数据；帧内 funding_interval 仍用于年化折算）。
type gateFuturesUpdate struct {
	Event  string `json:"event"`
	Result []struct {
		Contract         string  `json:"contract"`
		MarkPrice        string  `json:"mark_price"`
		T                int64   `json:"t"` // unix ms
		FundingRate      string  `json:"funding_rate"`
		FundingNextApply float64 `json:"funding_next_apply"` // unix s
		FundingInterval  float64 `json:"funding_interval"`   // s
	} `json:"result"`
}

// handleFutures 解析 futures.tickers 更新并发布报价。
func (g *GateWebSocket) handleFutures(ctx context.Context, data []byte, publish func(bus.Quote)) {
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
		// D-509 S3：帧内原生 funding 字段→fact（缺字段=缺省不造，venue 禁借用律）。
		if g.emitFacts != nil && item.FundingRate != "" {
			var rate float64
			if _, err := fmt.Sscanf(item.FundingRate, "%f", &rate); err != nil {
				log.Printf("[gate-ws] funding rate %q dirty: %v", item.FundingRate, err)
				continue
			}
			sym := canonicalFromGate(item.Contract)                      // BTCUSDT 形（与 REST 键 sym+USDT 同形）
			periods := fundingPeriodsPerDay(item.FundingInterval * 1000) // 帧内原生 interval
			fs := []fact.Fact{
				{Kind: fact.KindFunding, Venue: VenueGate, Symbol: sym,
					Value: annualizeFunding(rate, periods), Unit: fact.UnitPctAnnualized,
					Ts: ts, Src: "gate_ws_futures"},
			}
			if item.FundingNextApply > 0 {
				fs = append(fs, fact.Fact{Kind: fact.KindFundingNextSettle, Venue: VenueGate,
					Symbol: sym, Value: item.FundingNextApply, Unit: fact.UnitUnixSec, Ts: ts, Src: "gate_ws_futures"})
			}
			if err := g.emitFacts(ctx, fs); err != nil {
				log.Printf("[gate-ws] funding emit %s: %v", item.Contract, err)
			}
		}
	}
}

// canonicalFromGate 将 Gate 交易对/合约名（BTC_USDT）映射为 canonical（BTCUSDT）。
func canonicalFromGate(pair string) string {
	return strings.ReplaceAll(pair, "_", "")
}

// syms D-369 S2：gate WS 订阅符号集（env ∪ provider 动态集，连接期可增量）。
func (g *GateWebSocket) syms() []string { return g.cfg.effectiveSymbols("gate") }

// pairs 符号→gate 交易对后缀拼接（BTC → BTC_USDT）。
func pairsD369(syms []string, suffix string) []string {
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		out = append(out, s+suffix)
	}
	return out
}

// SetFactEmit 注入 fact 发射口（wiring 期；先于 Run）。
func (g *GateWebSocket) SetFactEmit(fn func(ctx context.Context, fs []fact.Fact) error) {
	g.emitFacts = fn
}
