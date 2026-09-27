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
	"slices"
	"strconv"
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
	cfg        Config
	publicURLs []string // perp bookTicker → /public/ws（D-517 fstream 拆分：高频盘口类）；有序候选
	marketURLs []string // perp markPrice → /market/ws（常规行情类）；同上
	spotURLs   []string // 同上
	dialer     *websocket.Dialer
	header     http.Header
	now        func() time.Time
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
	publicURLs := []string{scheme + "://" + u.Host + "/public/ws"}
	marketURLs := []string{scheme + "://" + u.Host + "/market/ws"}
	spotURLs := []string{scheme + "://" + u.Host + "/spotws/ws"}
	// cex-proxy 双端点 failover：BINANCE_PROXY_URL_BACKUP 置位→备用基址追加为第二候选
	//（同路径后缀映射；主端恢复后每连接尝试自主端起=自动回主）。
	// D-519 D6：备端 scheme 与 normWSBase 同语义归一（https→wss/http→ws/ws·wss 原样——
	// 内网明文备端 ws:// 不得被升 wss）。
	if b := normWSBase(os.Getenv("BINANCE_PROXY_URL_BACKUP")); b != "" {
		publicURLs = append(publicURLs, b+"/public/ws")
		marketURLs = append(marketURLs, b+"/market/ws")
		spotURLs = append(spotURLs, b+"/spotws/ws")
	}
	return &BinanceMarketWS{
		cfg:        cfg,
		publicURLs: publicURLs, marketURLs: marketURLs, spotURLs: spotURLs,
		dialer: d, header: h, now: time.Now,
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
	// D-529：perp bookTicker=907 符号最高帧率流，CF 收编链路+1 跳后被上游按
	// 「高帧率慢消费」杀连（15-30s 周期 1006 EOF 风暴）——订阅集排序对半拆 2
	// 连接降帧，meter 键带序号防告警身份合流。
	for i := 0; i < 2; i++ {
		i := i
		safego.Go(fmt.Sprintf("binance_market_ws:perp-public:%d", i), func() {
			defer wg.Done()
			b.runLoop(ctx, b.publicURLs, "perp", "bookTicker", i, publish)
		})
	}
	safego.Go("binance_market_ws:perp-market", func() {
		defer wg.Done()
		b.runLoop(ctx, b.marketURLs, "perp", "markPrice", -1, publish)
	})
	safego.Go("binance_market_ws:spot", func() {
		defer wg.Done()
		b.runLoop(ctx, b.spotURLs, "spot", "bookTicker", -1, publish)
	})
	wg.Wait()
}

// runLoop 单连接重连循环——D-511 S4：骨架收编 RunWS，本函数只装配协议钩子
// （分批 SUBSCRIBE 帧集/ack 分流/markPrice→bookTicker 分发/显式 token 头）。
// D-517 R1：kind=连接流类别（bookTicker|markPrice），venueType 恒喂 QuoteBus
// perp/spot——连接身份与 venue 语义分离（venueType 污染=QuoteBus 键断供）。
// shard≥0 时只订阅 effectiveSymbols 排序后的第 shard 段（两半对分；D-529）。
func (b *BinanceMarketWS) runLoop(ctx context.Context, urls []string, venueType, kind string, shard int, publish func(bus.Quote)) {
	meterKey := venueType + ":" + kind + ":" + urls[0]
	if shard >= 0 {
		meterKey = venueType + ":" + kind + ":" + strconv.Itoa(shard) + ":" + urls[0]
	}
	meter := newReconnectMeter(meterKey, b.cfg.AlertFn) // D-507 S2 扩：同款风暴告警
	RunWS(ctx, WSSpec{
		Name: mktWSName(venueType, kind, shard), URLs: urls, Dialer: b.dialer, Header: b.header,
		InitialSubs: func() []any {
			// 符号源与原 ticker 同源（effectiveSymbols：env ∪ SymProvider
			// 动态 watchlist）；watchlist 新增符号随下次重连订阅生效，60s REST 兜底补窗。
			syms := b.cfg.effectiveSymbols()
			if shard >= 0 {
				syms = shardSymbols(syms, shard)
			}
			params := make([]string, 0, len(syms))
			for _, sym := range syms {
				// D-517：按 kind 出订阅参数——bookTicker→public 路由、markPrice→market
				// 路由（funding 是 perp 语义，spot 连接恒 bookTicker 不进此分支）。
				// kind 不 lowercase：新端点流名大小写敏感（实证 btcusdt@markprice
				// /bookticker 全小写=ack 成功+零帧静默断流，与旧端点退役同形态）。
				params = append(params, strings.ToLower(sym+"usdt")+"@"+kind)
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
			if kind == "markPrice" && b.handleMarkPrice(ctx, data) {
				return // markPriceUpdate 帧已消费（market 连接先 markPrice 后 bookTicker 兜底）
			}
			b.handleBookTicker(data, venueType, publish)
		},
		OnReconnect: meter.record,
	})
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

// handleMarkPrice D-509 S2：markPriceUpdate 帧→funding 三 fact（rate 年化走
// FundingIntervalCache 真值、T=nextFundingTime ms→NextSettle、interval 同帧发）。
// 字段：e=markPriceUpdate、s=symbol、r=funding rate（string）、T=nextFundingTime ms。
// 返回是否已消费（非 markPrice 帧/缺关键字段/无 emitFacts → false 交给 bookTicker 面）。
func (b *BinanceMarketWS) handleMarkPrice(ctx context.Context, data []byte) bool {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	if e, _ := m["e"].(string); e != "markPriceUpdate" {
		return false
	}
	if b.emitFacts == nil {
		return true // 是 markPrice 帧但无发射口（测试/未装配）——已消费不泄漏给行情面
	}
	sym, _ := m["s"].(string)
	rateS, _ := m["r"].(string)
	if sym == "" || rateS == "" {
		return true // 缺关键字段：该 symbol funding 缺省不造（venue 禁借用律）
	}
	var rate float64
	if _, err := fmt.Sscanf(rateS, "%f", &rate); err != nil {
		log.Printf("[binance-mkt-ws] markPrice funding rate %q dirty: %v", rateS, err)
		return true
	}
	nftMs, _ := m["T"].(float64)
	periodsPerDay := fundingPeriodsPerDay(0)
	if b.intervals != nil {
		periodsPerDay = b.intervals.PeriodsPerDay(sym)
	}
	now := b.now()
	fs := []fact.Fact{
		{Kind: fact.KindFunding, Venue: VenueBinance, Symbol: sym,
			Value: annualizeFunding(rate, periodsPerDay), Unit: fact.UnitPctAnnualized,
			Ts: now, Src: "binance_ws_markprice"},
	}
	if nftMs > 0 {
		fs = append(fs,
			fact.Fact{Kind: fact.KindFundingNextSettle, Venue: VenueBinance, Symbol: sym,
				Value: nftMs / 1000, Unit: fact.UnitUnixSec, Ts: now, Src: "binance_ws_markprice"},
			fact.Fact{Kind: fact.KindFundingInterval, Venue: VenueBinance, Symbol: sym,
				Value: 86400 / float64(periodsPerDay), Unit: fact.UnitSeconds, Ts: now, Src: "binance_ws_markprice"})
	}
	if err := b.emitFacts(ctx, fs); err != nil {
		log.Printf("[binance-mkt-ws] funding emit %s: %v", sym, err)
	}
	return true
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

// mktWSName D-531 S1：日志身份含 shard 序号（shard<0 保原名），判别哪条连接被杀。
func mktWSName(venueType, kind string, shard int) string {
	name := "binance-mkt-ws:" + venueType + ":" + kind
	if shard >= 0 {
		name += ":" + strconv.Itoa(shard)
	}
	return name
}

// shardSymbols D-529：符号集排序后对半分桶（确定性——同输入同分桶，重连订阅集稳定）。
// shard∈{0,1}；0 取前半（奇数时多一个），1 取后半。
func shardSymbols(syms []string, shard int) []string {
	sorted := slices.Clone(syms)
	slices.Sort(sorted)
	half := (len(sorted) + 1) / 2
	if shard == 0 {
		return sorted[:half]
	}
	return sorted[half:]
}
