// binance_market_ws.go —— D-505 S1：Binance 公开报价 WS 流化（替代 REST 轮询主路径）。
// D-517 fstream 端点拆分三连接：perp bookTicker→/public/ws、perp markPrice→/market/ws、
// spot→/spotws/ws（host=cfg.BinanceBaseURL 的 scheme+host，同 BinanceUserWS.WSURL 取法——
// REST 路径段不进 WS URL；旧 /ws 单端点 4-23 退役=ack 成功+迁移流零帧静默断流）。
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
	"os"
	"strings"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
	"arb/internal/httpx"
	"arb/internal/safego"

	"github.com/gorilla/websocket"
)

// BinanceMarketWS Binance 公开行情 WS（D-517：fstream 端点拆分三连接订阅）。
type BinanceMarketWS struct {
	cfg         Config
	publicCands []WSCandidate // perp bookTicker → /public/ws（D-517 fstream 拆分：高频盘口类）；有序候选
	marketCands []WSCandidate // perp markPrice → /market/ws（常规行情类）；同上
	spotCands   []WSCandidate // 同上
	dialer      *websocket.Dialer
	header      http.Header
	now         func() time.Time
	// emitFacts D-509：funding fact 发射口（FactEmitter 去重节流；nil=不 emit）。
	emitFacts func(ctx context.Context, fs []fact.Fact) error
	// intervals D-509 S2：funding 周期缓存（fundingInfo 真值；nil=回退 3/天）。
	intervals *FundingIntervalCache
	// D-551 S2 现货订阅过滤面：infoURL=exchangeInfo 端点（默认 vision 公开域）；client
	// 按 host 收敛 token（代理=cfg.Client/原生=裸）；validSpot=有效符号缓存（nil=fail-open）。
	spotInfoURL    string
	spotInfoClient *http.Client
	validSpot      map[string]struct{}
	infoOnce       sync.Once
	spotInfoFetch  func(ctx context.Context, url string) (map[string]struct{}, error) // 测试注入点；nil=实拉 spotInfoURL
	tickerEmit     map[string]time.Time                                               // D-591 S6：vol24h 每符号节流（仅 ticker 连接 goroutine 访问）
	indexEmit      map[string]time.Time                                               // D-595 S1：index_price 每符号节流（仅 market 连接 goroutine 访问）
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
	publicURLs := []string{scheme + "://" + u.Host + "/public/ws"}
	marketURLs := []string{scheme + "://" + u.Host + "/market/ws"}
	spotURLs := []string{scheme + "://" + u.Host + "/spotws/ws"}
	// cex-proxy 双端点 failover：BINANCE_PROXY_URL_BACKUP 置位→备用基址追加为第二候选
	//（同路径后缀映射；主端恢复后每连接尝试自主端起=自动回主）。
	// D-519 D6：备端 scheme 与 normWSBase 同语义归一（https→wss/http→ws/ws·wss 原样——
	// 内网明文备端 ws:// 不得被升 wss）。
	proxyHosts := map[string]bool{u.Host: true}
	if b := normWSBase(os.Getenv("BINANCE_PROXY_URL_BACKUP")); b != "" {
		publicURLs = append(publicURLs, b+"/public/ws")
		marketURLs = append(marketURLs, b+"/market/ws")
		spotURLs = append(spotURLs, b+"/spotws/ws")
		if bu, err := url.Parse(b); err == nil {
			proxyHosts[bu.Host] = true
		}
	}
	// D-551 S1 逐流 env 覆盖：置位=env URL 前插首选候选，派生代理降候选#2；
	// 未置位=全量现行为零默认变更。
	if v := strings.TrimSpace(os.Getenv("ARB_BINANCE_PERP_PUBLIC_WS_URL")); v != "" {
		publicURLs = append([]string{v}, publicURLs...)
	}
	if v := strings.TrimSpace(os.Getenv("ARB_BINANCE_PERP_MARKET_WS_URL")); v != "" {
		marketURLs = append([]string{v}, marketURLs...)
	}
	if v := strings.TrimSpace(os.Getenv("ARB_BINANCE_SPOT_WS_URL")); v != "" {
		spotURLs = append([]string{v}, spotURLs...)
	}
	// D-551 S1 token 收敛：X-Proxy-Token 仅对代理 host 发出（币安原生端点空头）。
	toCands := func(urls []string) []WSCandidate {
		cs := make([]WSCandidate, len(urls))
		for i, raw := range urls {
			var hdr http.Header
			if pu, err := url.Parse(raw); err == nil && proxyHosts[pu.Host] {
				hdr = h
			}
			cs[i] = WSCandidate{URL: raw, Dialer: d, Header: hdr}
		}
		return cs
	}
	spotInfoURL := cfg.BinanceSpotInfoURL
	if spotInfoURL == "" {
		spotInfoURL = os.Getenv("ARB_BINANCE_SPOT_INFO_URL")
	}
	if spotInfoURL == "" {
		spotInfoURL = "https://data-api.binance.vision/api/v3/exchangeInfo"
	}
	// D-551 S2 token 收敛（REST 同裁定）：infoURL 指币安原生域换裸客户端防泄；
	// 指回代理 host 仍须 token+自签 CA（cfg.Client 同源）。
	spotInfoClient := cfg.Client
	if pu, err := url.Parse(spotInfoURL); err == nil && !proxyHosts[pu.Host] {
		spotInfoClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &BinanceMarketWS{
		cfg:         cfg,
		publicCands: toCands(publicURLs), marketCands: toCands(marketURLs), spotCands: toCands(spotURLs),
		dialer: d, header: h, now: time.Now, spotInfoURL: spotInfoURL, spotInfoClient: spotInfoClient,
	}
}

// Run 三连接并行（D-517：perp bookTicker→public、perp markPrice→market、spot→spotws），
// 阻塞至 ctx 取消（mirror gate_ws.Run）。
func (b *BinanceMarketWS) Run(ctx context.Context, publish func(bus.Quote)) {
	if b == nil {
		return // baseURL 解析失败静默（REST 兜底在跑）
	}
	var wg sync.WaitGroup
	wg.Add(4)
	// D-535：perp bookTicker 切 !bookTicker 聚合流——D-531 取证=binance 上游
	// 对逐符号高突发订阅短窗杀（分片/路径/源IP 全排除），E1 实测聚合流
	// 单订阅存活 ≥10min 零断；D-529 双拆随聚合流天然单连接退役。
	safego.Go("binance_market_ws:perp-public", func() {
		defer wg.Done()
		b.runLoop(ctx, b.publicCands, "perp", "bookTicker", publish)
	})
	safego.Go("binance_market_ws:perp-market", func() {
		defer wg.Done()
		b.runLoop(ctx, b.marketCands, "perp", "markPrice", publish)
	})
	safego.Go("binance_market_ws:spot", func() {
		defer wg.Done()
		b.ensureSpotInfo(ctx) // D-551 S2：首连前建立现货有效符号缓存
		b.runLoop(ctx, b.spotCands, "spot", "bookTicker", publish)
	})
	// D-591 S6：perp 24hrTicker 全量聚合流——vol24h facts 落库（radar 全量宇宙
	// 资格判定源；批处理 24hr REST 轮询退役）。
	safego.Go("binance_market_ws:perp-ticker", func() {
		defer wg.Done()
		b.runLoop(ctx, b.marketCands, "perp", "ticker", publish)
	})
	wg.Wait()
}

// runLoop 单连接重连循环——D-511 S4：骨架收编 RunWS，本函数只装配协议钩子
// （分批 SUBSCRIBE 帧集/ack 分流/markPrice→bookTicker 分发/显式 token 头）。
// D-517 R1：kind=连接流类别（bookTicker|markPrice），venueType 恒喂 QuoteBus
// perp/spot——连接身份与 venue 语义分离（venueType 污染=QuoteBus 键断供）。
// D-535：perp bookTicker 订阅单帧 "!bookTicker" 聚合流（全市场同事件格式直转下游）。
func (b *BinanceMarketWS) runLoop(ctx context.Context, cands []WSCandidate, venueType, kind string, publish func(bus.Quote)) {
	meter := newReconnectMeter(venueType+":"+kind+":"+cands[0].URL, b.cfg.AlertFn) // D-507 S2 扩：同款风暴告警
	RunWS(ctx, WSSpec{
		Name: "binance-mkt-ws:" + venueType + ":" + kind, Candidates: cands, DepKey: "binance:ws-" + venueType + "-" + kind, // DepKey D-579 dephealth 记账键
		AlertFn: b.cfg.AlertFn, // D-074 ①：冷却循环升级「通道死」告警同管道
		InitialSubs: func() []any {
			var params []string
			if venueType == "perp" && kind == "bookTicker" {
				// D-535 E1：binance 上游杀招针对逐符号订阅突发——聚合流单订阅存活。
				params = []string{"!bookTicker"}
			} else if venueType == "perp" && kind == "ticker" {
				// D-591 S6：24hrTicker 全市场聚合流（radar 宇宙枚举全量所需，不过滤——
				// 资格判定须覆盖未监控新上市符号；灌水由每符号 15min 节流控制）。
				params = []string{"!ticker@arr@1s"}
			} else if venueType == "perp" && kind == "markPrice" {
				// D-582：markPrice 逐符号订阅同中 D-535 杀招（上游按连接 epoch 概率性
				// 裁断，wscloseprobe 同参数集复现 frames=1 后 1006 被杀）——改全市场
				// 聚合流单订阅（数组帧与单帧元素同构，handleMarkPrice 双形解析+有效集过滤；
				// 流名字面量禁规范化——binance-ws-stream-name-case-sensitive）。
				params = []string{"!markPrice@arr@1s"}
			} else {
				// 符号源与原 ticker 同源（effectiveSymbols：env ∪ SymProvider
				// 动态 watchlist）；watchlist 新增符号随下次重连订阅生效，60s REST 兜底补窗。
				syms := b.cfg.effectiveSymbols("binance")
				params = make([]string, 0, len(syms))
				for _, sym := range syms {
					if venueType == "spot" && !b.spotOK(sym) {
						continue // D-551 S2：现货不存在符号不订（白占上游突发宽度）
					}
					// D-517：按 kind 出订阅参数——bookTicker→public 路由、markPrice→market
					// 路由（funding 是 perp 语义，spot 连接恒 bookTicker 不进此分支）。
					// kind 不 lowercase：新端点流名大小写敏感（实证 btcusdt@markprice
					// /bookticker 全小写=ack 成功+零帧静默断流，与旧端点退役同形态）。
					params = append(params, strings.ToLower(sym+"usdt")+"@"+kind)
				}
			}
			// D-509 merge 面修复：单帧 SUBSCRIBE 超服务端解析上限（实拍 1008 +
			// "Invalid JSON …column 4096"）→ 分批多帧订阅（RunWS 逐帧发送语义已兼容）。
			var frames []any
			for i, id := 0, 1; i < len(params); i += binanceMktSubChunk {
				end := min(i+binanceMktSubChunk, len(params))
				frames = append(frames, map[string]any{
					"method": "SUBSCRIBE", "params": params[i:end], "id": id,
				})
				id++
			}
			return frames
		},
		OnFrame: func(data []byte) {
			if isSubscribeAck(data) {
				return // {result:null,id:1} ack；{result:…,error} 已在 isSubscribeAck 记日志
			}
			if kind == "ticker" {
				// D-080：24hrTicker 数组帧→vol24h facts（D-591 S6 handler 自创建日起
				// 零调用者，OnFrame 缺 ticker 分流=帧落 handleBookTicker 被静默丢弃；
				// ticker 连接不订 bookTicker，全帧归 handleTicker，解析失败静默）。
				b.handleTicker(ctx, data)
				return
			}
			if kind == "markPrice" && b.handleMarkPrice(ctx, data) {
				return // markPriceUpdate 帧已消费（market 连接先 markPrice 后 bookTicker 兜底）
			}
			b.handleBookTicker(data, venueType, publish)
		},
		OnReconnect: meter.record,
	})
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

// binanceMktSubChunk SUBSCRIBE 单帧参数上限（服务端单帧解析上限约 4KB，实拍 column 4096 截断）。
const binanceMktSubChunk = 120

// SetFactEmit 注入 fact 发射口（wiring 期；先于 Run）。
func (b *BinanceMarketWS) SetFactEmit(fn func(ctx context.Context, fs []fact.Fact) error) {
	b.emitFacts = fn
}

// SetFundingIntervals 注入 funding 周期缓存（D-301a boot 真值；BinanceFundingHistory 同源）。
func (b *BinanceMarketWS) SetFundingIntervals(c *FundingIntervalCache) { b.intervals = c }
