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
	"os"
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
	// D-537：订阅边界合法 pair 白名单（spot/futures 各持本 channel 集不混）；
	// spot.tickers 对任一非法 pair 静默拒整批——订阅前必须滤。
	spotWL    *gatePairWhitelist
	futuresWL *gatePairWhitelist
	// D-541 S3：proxied=代理模式（候选表追加 native 直连兜底位）；directDialer=
	// 兜底位裸拨号件（token 不泄原生域）。非代理模式单 native 候选无兜底。
	proxied      bool
	directDialer *websocket.Dialer
	// D-507：时距字段化（默认=ws_keepalive.go 常量族；测试覆写缩小观察窗）。
	readIdle         time.Duration
	pingEvery        time.Duration
	reconnectBackoff time.Duration
	// emitFacts D-509：funding fact 发射口（FactEmitter 去重节流；nil=不 emit）。
	emitFacts func(ctx context.Context, fs []fact.Fact) error
	// onContractInfo D-570 包C S6④：futures.contract_info 帧→按 contract 失效回调
	// （exec spec 缓存+collect quanto 缓存双失效在接线层闭包；nil=仅订阅不联动）。
	onContractInfo func(contract string)
}

// NewGateWebSocket 创建 Gate WS 采集器（ARB_CEX_WS_BASE 置位→经 cex-proxy /gatespotws 与 /v4/ws 前缀）。
// D-541 S0：ARB_GATE_WS_MODE=direct→强制原生直连（per-venue 止血开关，缺省 proxy=现行，
// 切回=env 还原；不动 okx/binance 装配）。
func NewGateWebSocket(cfg Config) *GateWebSocket {
	direct := os.Getenv("ARB_GATE_WS_MODE") == "direct"
	proxied := cexWSBase() != "" && !direct
	d, h := wsDialer(proxied)
	spotWL, futuresWL := newGatePairWhitelists(cfg)
	spotURLs := wsViaProxyURLs("/gatespotws/ws/v4/", gateSpotWSURL)
	futuresURLs := wsViaProxyURLs("/v4/ws/usdt", gateFuturesWSURL)
	if direct {
		spotURLs = []string{gateSpotWSURL}
		futuresURLs = []string{gateFuturesWSURL}
	}
	dd, _ := wsDialer(false) // D-541 S3：直连兜底位裸拨号件+nil header
	return &GateWebSocket{cfg: cfg, dialer: d, header: h,
		spotURLs: spotURLs, futuresURLs: futuresURLs,
		spotWL: spotWL, futuresWL: futuresWL,
		proxied: proxied, directDialer: dd,
		readIdle: quoteWSReadIdle, pingEvery: quoteWSPingEvery, reconnectBackoff: quoteWSReconnectBackoff}
}

// Run 连接 Gate spot + futures WS 并持续推送报价到 publish。阻塞至 ctx 取消。
func (g *GateWebSocket) Run(ctx context.Context, publish func(bus.Quote)) {
	// D-537：启动同步首拉白名单（失败=不过滤，单次拉取上限 10s），随后后台 1h 刷新。
	g.spotWL.refresh(ctx)
	g.futuresWL.refresh(ctx)
	safego.Go("gate_ws:pair-wl-refresh", func() {
		refreshGateWhitelists(ctx, g.spotWL, g.futuresWL)
	})
	var wg sync.WaitGroup
	wg.Add(2)
	safego.Go("gate_ws:spot-loop", func() {
		defer wg.Done()
		g.runLoop(ctx, g.spotURLs, []string{"spot.tickers"}, gateSpotWSURL, g.spotWL, g.handleSpot, publish, nil)
	})
	safego.Go("gate_ws:futures-loop", func() {
		defer wg.Done()
		// D-570 包C：futures 连接订阅面=4 频道同连接（清单/固定帧入常量函数=A14/A16 锚）。
		g.runLoop(ctx, g.futuresURLs, gateFuturesTickerChannels, gateFuturesWSURL, g.futuresWL, g.handleFuturesDispatch, publish, gateFuturesExtraSubs())
	})
	wg.Wait()
}

// candidates D-541 S3：候选表=[代理路由…, native 直连兜底位]——兜底位裸
// dialer+nil Header（X-Proxy-Token 不泄 api.gateio.ws 原生域）；非代理模式=
// urls 原样（单 native 候选，冷却机制同构无害）。
func (g *GateWebSocket) candidates(urls []string, native string) []WSCandidate {
	cands := make([]WSCandidate, 0, len(urls)+1)
	for _, u := range urls {
		cands = append(cands, WSCandidate{URL: u, Dialer: g.dialer, Header: g.header})
	}
	if g.proxied {
		cands = append(cands, WSCandidate{URL: native, Dialer: g.directDialer, Header: nil})
	}
	return cands
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

// gateFuturesTickerChannels D-570 包C：futures 连接 pairs 型频道清单
// （tickers=报价+funding 帧职责；book_ticker=真实盘口 S8——同 wl.filter 集）。
var gateFuturesTickerChannels = []string{"futures.tickers", "futures.book_ticker"}

// gateFuturesExtraSubs D-570 包C：futures 连接固定订阅帧（["!all"] 公共无 auth，
// 不经白名单）——contract_info=规格失效推送 S6④；adl_warning=ADL 预警 S7。
func gateFuturesExtraSubs() []any {
	return []any{
		subscribePayload("futures.contract_info", []string{"!all"}),
		subscribePayload("futures.adl_warning", []string{"!all"}),
	}
}

// gateSubFrames pairs 集各频道一帧 + extra 固定帧（A16 锚：所有 pairs 频道共用同一过滤后集）。
func gateSubFrames(channels []string, pairs []string, extra []any) []any {
	frames := make([]any, 0, len(channels)+len(extra))
	for _, ch := range channels {
		frames = append(frames, subscribePayload(ch, pairs))
	}
	return append(frames, extra...)
}

// runLoop 单连接重连循环——D-511 S3：骨架收编 RunWS（dial/保活/读循环/退避一处钉死），
// 本函数只装配协议钩子（候选=urls 代理件+proxied 时 native 直连兜底位——R4/R3）。
func (g *GateWebSocket) runLoop(ctx context.Context, urls []string, channels []string, native string, wl *gatePairWhitelist, handle func(context.Context, []byte, func(bus.Quote)), publish func(bus.Quote), extraSubs []any) {
	meter := newReconnectMeter(urls[0], g.cfg.AlertFn)
	// subscribed 差集表为每连接状态——OnConnect 每拨号重置（重连全量重订阅语义不变）。
	var subscribed map[string]bool
	RunWS(ctx, WSSpec{
		Name: "gate-ws:" + urls[0], Candidates: g.candidates(urls, native),
		ReadIdle: g.readIdle, PingEvery: g.pingEvery, ReconnectBackoff: g.reconnectBackoff,
		OnConnect: func() { subscribed = map[string]bool{} },
		InitialSubs: func() []any {
			// D-537：订阅前白名单过滤——spot.tickers 对任一非法 pair 静默拒整批。
			all := pairsD369(g.syms(), "_USDT")
			pairs := wl.filter(all)
			if dropped := len(all) - len(pairs); dropped > 0 {
				log.Printf("[gate-ws] 订阅对过滤 dropped=%d/%d（非gate合法pair）", dropped, len(all))
			}
			for _, p := range pairs {
				subscribed[p] = true
			}
			// D-570 包C：多频道同连接——pairs 集各频道一帧 + extraSubs 固定帧
			// （contract_info/adl_warning "!all" 不经白名单）。
			return gateSubFrames(channels, pairs, extraSubs)
		},
		// D-369 S2：provider 符号集变化 → ping tick 差集补订（新符号 only；服务端拒则读报错走重连）
		OnAliveTick: func(s Sender) {
			var newPairs []string
			// D-537：差集同过白名单——非法 pair 不进 subscribed 不发帧（防静默拒批+重试循环）。
			for _, p := range wl.filter(pairsD369(g.syms(), "_USDT")) {
				if !subscribed[p] {
					newPairs = append(newPairs, p)
				}
			}
			if len(newPairs) > 0 {
				sent := true
				for _, ch := range channels { // D-570 包C S8：新 pairs 对每个 pairs 频道补订
					if g.subscribeChannel(s, ch, newPairs) != nil {
						sent = false
					}
				}
				if sent {
					for _, p := range newPairs {
						subscribed[p] = true
					}
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

// SetOnContractInfo D-570 包C S6④：注入 contract_info 失效回调（wiring 期；先于 Run；nil 安全）。
func (g *GateWebSocket) SetOnContractInfo(fn func(contract string)) { g.onContractInfo = fn }
