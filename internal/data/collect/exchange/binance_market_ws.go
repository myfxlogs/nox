// binance_market_ws.go —— D-505 S1：Binance 公开报价 WS 流化（替代 REST 轮询主路径）。
// fstream 端点拆分三连接：perp bookTicker→/public/ws、perp markPrice→/market/ws、
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
	"log"
	"net/http"
	"net/url"
	"nox/internal/infra/dephealth"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
	"nox/internal/platform/httpx"
	"nox/internal/platform/safego"

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
	// 现货订阅过滤面：infoURL=exchangeInfo 端点（默认 vision 公开域）；client
	// 按 host 收敛 token（代理=cfg.Client/原生=裸）；validSpot=有效符号缓存（nil=fail-open）。
	spotInfoURL    string
	spotInfoClient *http.Client
	validSpot      map[string]struct{}
	infoOnce       sync.Once
	spotInfoFetch  func(ctx context.Context, url string) (map[string]struct{}, error) // 测试注入点；nil=实拉 spotInfoURL
	tickerEmit     map[string]time.Time                                               // D-591 S6：vol24h 每符号节流（仅 ticker 连接 goroutine 访问）
	indexEmit      map[string]time.Time                                               // D-595 S1：index_price 每符号节流（仅 market 连接 goroutine 访问）
	// degradedFn D-109：死流判定注入点（REST 兜底看门狗用；nil=dephealth.Degraded 生产读口）。
	degradedFn func(key string) bool
	// R1①：帧年龄闸——dephealth.Degraded 需连败 3 次才红，但「dial 成功→ack→
	// 静默/60s-EOF」形态 dial 每次都 Mark(true) 连败归零=兜底永不点火；改看帧年龄
	// （now-lastFrame>staleAfter=死流），dial 死与静默两形态同覆，流复绿帧到闸自落。
	// unixnano；markPrice/ticker 连接 goroutine 写、rest-fallback goroutine 读。
	lastFrameMarkPrice atomic.Int64
	lastFrameTicker    atomic.Int64
	// restNative D-109 R1②：币安原生域 REST 裸 client——cfg.Client（httpx tokenRT）
	// 会把 X-Proxy-Token 注入每请求，带向 fapi.binance.com=凭据出域（D-106 红线）。
	restNative *http.Client
	// REST 兜底轨节流 map（仅 rest-fallback goroutine 访问；与 WS 轨 map 分离防跨程竞争）。
	indexEmitREST  map[string]time.Time
	tickerEmitREST map[string]time.Time
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
	var publicURLs, marketURLs, spotURLs []string
	proxyHosts := map[string]bool{}
	if isNativeBinance(u.Host) {
		// 修补：原生域 live-subscribe 端点——/public|spotws 是 cex-proxy 路由形制；
		// proxyHosts 不得含原生 host（否则 X-Proxy-Token 随握手漏给币安）。
		// 10-21 直探实锤（死循环取证票）：原生 /ws 对 !markPrice@arr/!ticker@arr
		// ack 收讫但零帧停推（venue 端点迁移/选择性强拆——!bookTicker 同端点仍活），
		// fstream 原生 /market/ws 实测产帧（biz>0）——markPrice/ticker 候选改 /market/ws。
		publicURLs = []string{"wss://fstream.binance.com/ws"}
		marketURLs = []string{"wss://fstream.binance.com/market/ws"}
		spotURLs = []string{"wss://stream.binance.com:9443/ws"}
	} else {
		publicURLs = []string{scheme + "://" + u.Host + "/public/ws"}
		marketURLs = []string{scheme + "://" + u.Host + "/market/ws"}
		spotURLs = []string{scheme + "://" + u.Host + "/spotws/ws"}
		// cex-proxy 双端点 failover：备基址置位→备用基址追加为第二候选
		//（同路径后缀映射；主端恢复后每连接尝试自主端起=自动回主）。
		// D6：备端 scheme 与 normWSBase 同语义归一（https→wss/http→ws/ws·wss 原样——
		// 内网明文备端 ws:// 不得被升 wss）。
		proxyHosts[u.Host] = true
		if b := normWSBase(ep.BinanceProxyBackup); b != "" {
			publicURLs = append(publicURLs, b+"/public/ws")
			marketURLs = append(marketURLs, b+"/market/ws")
			spotURLs = append(spotURLs, b+"/spotws/ws")
			if bu, err := url.Parse(b); err == nil {
				proxyHosts[bu.Host] = true
			}
		}
	}
	// 逐流覆盖（D-145 注入）：置位=配置 URL 前插首选候选，派生代理降候选#2；
	// 未置位=全量现行为零默认变更。
	if v := strings.TrimSpace(ep.PerpPublicWSURL); v != "" {
		publicURLs = append([]string{v}, publicURLs...)
	}
	if v := strings.TrimSpace(ep.PerpMarketWSURL); v != "" {
		marketURLs = append([]string{v}, marketURLs...)
	}
	if v := strings.TrimSpace(ep.SpotWSURL); v != "" {
		spotURLs = append([]string{v}, spotURLs...)
	}
	// token 收敛：X-Proxy-Token 仅对代理 host 发出（币安原生端点空头）。
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
		spotInfoURL = "https://data-api.binance.vision/api/v3/exchangeInfo"
	}
	// token 收敛（REST 同裁定）：infoURL 指币安原生域换裸客户端防泄；
	// 指回代理 host 仍须 token+自签 CA（cfg.Client 同源）。
	spotInfoClient := cfg.Client
	if pu, err := url.Parse(spotInfoURL); err == nil && !proxyHosts[pu.Host] {
		spotInfoClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &BinanceMarketWS{
		cfg:         cfg,
		publicCands: toCands(publicURLs), marketCands: toCands(marketURLs), spotCands: toCands(spotURLs),
		dialer: d, header: h, now: time.Now, spotInfoURL: spotInfoURL, spotInfoClient: spotInfoClient,
		restNative: &http.Client{Timeout: 15 * time.Second},
	}
}

// Run 三连接并行（D-517：perp bookTicker→public、perp markPrice→market、spot→spotws），
// 阻塞至 ctx 取消（mirror gate_ws.Run）。
func (b *BinanceMarketWS) Run(ctx context.Context, publish func(bus.Quote)) {
	// R1①：帧年龄闸基线=采集器启动刻——若订阅从未交付帧（含开集即静默形态），
	// 启动刻起算超龄即判定死流；首帧到达后改写为真实帧刻。
	b.lastFrameMarkPrice.Store(b.now().UnixNano())
	b.lastFrameTicker.Store(b.now().UnixNano())
	if b == nil {
		return // baseURL 解析失败静默（REST 兜底在跑）
	}
	var wg sync.WaitGroup
	wg.Add(6)
	// perp bookTicker 切 !bookTicker 聚合流——D-531 取证=binance 上游
	// 对逐符号高突发订阅短窗杀（分片/路径/源IP 全排除），E1 实测聚合流
	// 单订阅存活 ≥10min 零断；D-529 双拆随聚合流天然单连接退役。
	safego.GoLoopPlain("binance_market_ws:perp-public", func() {
		b.runLoop(ctx, b.publicCands, "perp", "bookTicker", publish, mktWSDataIdle)
		wg.Done() // D-115：尾部非 defer——panic 重启不预支 Done（计数器只随逻辑循环终结 -1）
	})
	safego.GoLoopPlain("binance_market_ws:perp-market", func() {
		b.runLoop(ctx, b.marketCands, "perp", "markPrice", publish, mktWSDataIdle)
		wg.Done()
	})
	safego.GoLoopPlain("binance_market_ws:spot", func() {
		b.ensureSpotInfo(ctx)                                         // D-551 S2：首连前建立现货有效符号缓存
		b.runLoop(ctx, b.spotCands, "spot", "bookTicker", publish, 0) // spot 低频阈不动（票面禁线）
		wg.Done()
	})
	// perp 24hrTicker 全量聚合流——vol24h facts 落库（radar 全量宇宙
	// 资格判定源；批处理 24hr REST 轮询退役）。
	safego.GoLoopPlain("binance_market_ws:perp-ticker", func() {
		b.runLoop(ctx, b.marketCands, "perp", "ticker", publish, mktWSDataIdle)
		wg.Done()
	})
	b.startIndivBookTickers(ctx, publish, &wg) // armed/watchlist 腿逐符号增订（indiv 件）
	// 死流 REST 看门狗兜底——dephealth 判 markPrice/ticker 流红才低频拉
	// REST 补观测 facts，复绿即停零轮询（D-004 例外留痕 decisions.md D-109）。
	wg.Add(1)
	safego.GoLoopPlain("binance_market_ws:rest-fallback", func() {
		b.runRestFallback(ctx)
		wg.Done()
	})
	wg.Wait()
}

// runLoop 单连接重连循环——D-511 S4：骨架收编 RunWS，本函数只装配协议钩子
// （分批 SUBSCRIBE 帧集/ack 分流/markPrice→bookTicker 分发/显式 token 头）。
// kind=连接流类别（bookTicker|markPrice），venueType 恒喂 QuoteBus
// perp/spot——连接身份与 venue 语义分离（venueType 污染=QuoteBus 键断供）。
// perp bookTicker 订阅单帧 "!bookTicker" 聚合流（全市场同事件格式直转下游）。
func (b *BinanceMarketWS) runLoop(ctx context.Context, cands []WSCandidate, venueType, kind string, publish func(bus.Quote), dataIdle time.Duration) {
	meter := newReconnectMeter(venueType+":"+kind+":"+cands[0].URL, b.cfg.AlertFn) // D-507 S2 扩：同款风暴告警
	RunWS(ctx, WSSpec{
		Name: "binance-mkt-ws:" + venueType + ":" + kind, Candidates: cands, DepKey: "binance:ws-" + venueType + "-" + kind, // DepKey D-579 dephealth 记账键
		DataIdle: dataIdle,      // 票名未注 D#：mkt-ws 阈降档票——perp 1s 频谱族 3min 判死盲窗（venue epoch 裁断第四袭实证——10min 阈白送盲窗）；0=回退 quoteWSDataIdle（spot 低频阈不动）
		AlertFn:  b.cfg.AlertFn, // D-074 ①：冷却循环升级「通道死」告警同管道
		InitialSubs: func() []any {
			var params []string
			if venueType == "perp" && kind == "bookTicker" {
				// E1：binance 上游杀招针对逐符号订阅突发——聚合流单订阅存活。
				params = []string{"!bookTicker"}
			} else if venueType == "perp" && kind == "ticker" {
				// 24hrTicker 全市场聚合流（radar 宇宙枚举全量所需，不过滤——
				// 资格判定须覆盖未监控新上市符号；灌水由每符号 15min 节流控制）。
				// 复审实拍：futures 无 "!ticker@arr@1s" 变体（ack 后零帧，@1s 仅 markPrice
				// 有），流名=文档 All Market Tickers "!ticker@arr"（1000ms）。
				params = []string{"!ticker@arr"}
			} else if venueType == "perp" && kind == "markPrice" {
				// markPrice 逐符号订阅同中 D-535 杀招（上游按连接 epoch 概率性
				// 裁断，wscloseprobe 同参数集复现 frames=1 后 1006 被杀）——改全市场
				// 聚合流单订阅（数组帧与单帧元素同构，handleMarkPrice 双形解析+有效集过滤；
				// 流名字面量禁规范化——binance-ws-stream-name-case-sensitive）。
				// 10-19 直探实锤：!markPrice@arr@1s = ack{result:null} 收讫+零帧
				// （venue 停推变体），!markPrice@arr 同窗复帧——降 @1s 变体。
				params = []string{"!markPrice@arr"}
			} else {
				// 符号源与原 ticker 同源（effectiveSymbols：env ∪ SymProvider
				// 动态 watchlist）；watchlist 新增符号随下次重连订阅生效，60s REST 兜底补窗。
				syms := b.cfg.effectiveSymbols("binance")
				params = make([]string, 0, len(syms))
				for _, sym := range syms {
					if venueType == "spot" && !b.spotOK(sym) {
						continue // D-551 S2：现货不存在符号不订（白占上游突发宽度）
					}
					// 按 kind 出订阅参数——bookTicker→public 路由、markPrice→market
					// 路由（funding 是 perp 语义，spot 连接恒 bookTicker 不进此分支）。
					// kind 不 lowercase：新端点流名大小写敏感（实证 btcusdt@markprice
					// /bookticker 全小写=ack 成功+零帧静默断流，与旧端点退役同形态）。
					params = append(params, strings.ToLower(sym+"usdt")+"@"+kind)
				}
			}
			// merge 面修复：单帧 SUBSCRIBE 超服务端解析上限（实拍 1008 +
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
		// binance 业务帧=被消费的本类帧——ticker 连接=24hrTicker 帧解析成
		// 功（vol24h fact 面）；markPrice 连接=markPriceUpdate 帧（fallback 到
		// bookTicker 产出 quote 亦计）；bookTicker 连接=bookTicker 解析产 quote。
		// subscribe ack/解析失败帧不计业务帧（遮订阅死病灶同 okx）。
		OnFrame: func(data []byte) bool {
			if isSubscribeAck(data) {
				return false // {result:null,id:1} ack；{result:…,error} 已在 isSubscribeAck 记日志
			}
			// D-109 R1③：帧到喂 dephealth（节流件内建 1/min/键）——
			// 「连接活帧死」形态 lastEventUnix 前移可证，解禁判据机读达标。
			dephealth.MarkFrameOK("binance:ws-" + venueType + "-" + kind)
			if kind == "ticker" {
				b.lastFrameTicker.Store(b.now().UnixNano()) // D-109 R1①：帧到=流活（含解析失败帧——有流量即非死流）
				// 24hrTicker 数组帧→vol24h facts（D-591 S6 handler 自创建日起
				// 零调用者，OnFrame 缺 ticker 分流=帧落 handleBookTicker 被静默丢弃；
				// ticker 连接不订 bookTicker，全帧归 handleTicker，解析失败静默）。
				return b.handleTicker(ctx, data)
			}
			if kind == "markPrice" {
				b.lastFrameMarkPrice.Store(b.now().UnixNano()) // D-109 R1①
			}
			if kind == "markPrice" && b.handleMarkPrice(ctx, data) {
				return true // markPriceUpdate 帧已消费（market 连接先 markPrice 后 bookTicker 兜底）
			}
			return b.handleBookTicker(data, venueType, publish)
		},
		OnReconnect: meter.record,
	})
}

// binanceMktSubChunk SUBSCRIBE 单帧参数上限（服务端单帧解析上限约 4KB，实拍 column 4096 截断）。
const binanceMktSubChunk = 120

// SetFactEmit 注入 fact 发射口（wiring 期；先于 Run）。
func (b *BinanceMarketWS) SetFactEmit(fn func(ctx context.Context, fs []fact.Fact) error) {
	b.emitFacts = fn
}

// SetFundingIntervals 注入 funding 周期缓存（D-301a boot 真值；BinanceFundingHistory 同源）。
func (b *BinanceMarketWS) SetFundingIntervals(c *FundingIntervalCache) { b.intervals = c }
