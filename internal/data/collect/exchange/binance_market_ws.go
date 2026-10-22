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
	// perp 存在性闸（normalize 残件②）：validPerp=perp exchangeInfo TRADING
	// 集（nil=fail-open——目录缺期不阻塞采集）；perpOnce 拉一次缓存。
	// infoURL=钦定端点（cfg.BinanceFuturesInfoURL——代理域/原生域皆可指）；
	// client 同 spotInfoClient 收敛裁定（代理域 token+CA/原生域裸）。
	perpInfoURL    string
	perpInfoClient *http.Client
	validPerp      map[string]struct{}
	perpOnce       sync.Once
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
	// perp 目录端点同裁：钦定 BinanceFuturesInfoURL（D-127 P2），空=官方默认；
	// 客户端按 host 收敛——代理域走 cfg.Client（token+CA），原生域裸 client 防泄。
	perpInfoURL := cfg.BinanceFuturesInfoURL
	if perpInfoURL == "" {
		perpInfoURL = "https://fapi.binance.com/fapi/v1/exchangeInfo"
	}
	perpInfoClient := cfg.Client
	if pu, err := url.Parse(perpInfoURL); err == nil && !proxyHosts[pu.Host] {
		perpInfoClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &BinanceMarketWS{
		cfg:         cfg,
		publicCands: toCands(publicURLs), marketCands: toCands(marketURLs), spotCands: toCands(spotURLs),
		dialer: d, header: h, now: time.Now, spotInfoURL: spotInfoURL, spotInfoClient: spotInfoClient,
		perpInfoURL: perpInfoURL, perpInfoClient: perpInfoClient,
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

// SetFactEmit 注入 fact 发射口（wiring 期；先于 Run）。
func (b *BinanceMarketWS) SetFactEmit(fn func(ctx context.Context, fs []fact.Fact) error) {
	b.emitFacts = fn
}

// SetFundingIntervals 注入 funding 周期缓存（D-301a boot 真值；BinanceFundingHistory 同源）。
func (b *BinanceMarketWS) SetFundingIntervals(c *FundingIntervalCache) { b.intervals = c }
