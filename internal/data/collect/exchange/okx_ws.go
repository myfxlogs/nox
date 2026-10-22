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

	"nox/internal/data/bus"
	"nox/internal/data/fact"

	"github.com/gorilla/websocket"
)

// okxPublicWSURL OKX 公共 WS 端点（spot+perp tickers 同连接）。
const okxPublicWSURL = "wss://ws.okx.com:8443/ws/v5/public"

// OKXWebSocket 订阅 OKX tickers 频道（spot + perp）。
type OKXWebSocket struct {
	cfg    Config
	dialer *websocket.Dialer
	header http.Header // 代理模式=X-Proxy-Token 握口头；直连=nil
	wsURLs []string    // 有序候选（cex-proxy 双端点 failover；[0]=主端）；测试覆写指向 fake server
	// 时距字段化（默认=ws_keepalive.go 常量族；测试覆写缩小观察窗）。
	readIdle         time.Duration
	pingEvery        time.Duration
	reconnectBackoff time.Duration
	dataIdle         time.Duration // D-154 g)：业务帧看门狗字段化（测试缩窗同 D-507 先例）
	// emitFacts D-509：funding fact 发射口（FactEmitter 去重节流；nil=不 emit）。
	emitFacts func(ctx context.Context, fs []fact.Fact) error
	// catalog D-524：instruments 存在性闸（watchlist 方言符号归一化后过目录，不伪造）。
	catalog *okxInstCatalog
	// OnInstrumentChange instruments 频道变更帧回调，nil=不通知。
	OnInstrumentChange func(instID string)
	// pulseFn D-155 方案A：活性脉冲打点口（qbus.Touch 接线；nil=不记录）。
	// mark-price（SWAP）/index-tickers（spot uly）帧到即调——只证订阅活不写报价值。
	pulseFn func(symbol, venueKey string)
	// DeliverySymbolsFn D-649续④：armed 交割腿订阅源（okx USD_UM 面——watchlist
	// 方言件只产 spot/perp 候选，交割 instId 由 armed 组合毕业档 provenance 注入；
	// nil=不订）。返回 instId→报价 canonical（hint.SellCanonical 同键归一）。
	DeliverySymbolsFn func(ctx context.Context) []DeliverySub
	// delivCanon instId→canonical 映射（subscribeArgs 每次重建；读侧在
	// handleMessage tickers 分流）。
	delivCanon map[string]string
}

// NewOKXWebSocket 创建 OKX WS 采集器（NOX_CEX_WS_BASE 置位→经 cex-proxy /okxws 前缀）。
func NewOKXWebSocket(cfg Config) *OKXWebSocket {
	proxied := cexWSBase() != ""
	d, h := wsDialer(proxied)
	return &OKXWebSocket{cfg: cfg, dialer: d, header: h,
		wsURLs:   wsViaProxyURLs("/okxws/ws/v5/public", okxPublicWSURL),
		readIdle: quoteWSReadIdle, pingEvery: quoteWSPingEvery, reconnectBackoff: quoteWSReconnectBackoff,
		dataIdle: mktWSDataIdle, // 2762/2825 案：静默停帧谱——10min 阈送盲窗（mt-health 5m 先于骨架检出=检出延迟反超），3min 对齐 mkt-ws 降档先例（okx tickers ≥1/s 频谱，3min 零帧=订阅态丢非静默期）
		catalog:  newOKXInstCatalog(cfg)}
}

// Run 连接 OKX WS 并持续推送报价到 publish 回调。阻塞至 ctx 取消。
// 骨架收编 RunWS——dial/保活/读循环/退避一处钉死，本文件只留协议钩子。
func (o *OKXWebSocket) Run(ctx context.Context, publish func(bus.Quote)) {
	meter := newReconnectMeter(o.wsURLs[0], o.cfg.AlertFn)
	// subscribed 差集表为每连接状态——OnConnect 每拨号重置（重连全量重订阅语义不变）。
	var subscribed map[string]bool
	// quarantined 隔离表进程级（重连不重置）——目录判决对同符号稳定，逐符号只记一行。
	quarantined := map[string]bool{}
	RunWS(ctx, WSSpec{
		Name: "okx-ws", URLs: o.wsURLs, Dialer: o.dialer, Header: o.header,
		DepKey:   "okx:ws-market", // D-579：dephealth 记账键
		AlertFn:  o.cfg.AlertFn,   // D-074 ①：冷却循环升级「通道死」告警同管道
		ReadIdle: o.readIdle, PingEvery: o.pingEvery, ReconnectBackoff: o.reconnectBackoff,
		DataIdle:  o.dataIdle, // D-154：业务帧窗（默认 quoteWSDataIdle）
		OnConnect: func() { subscribed = map[string]bool{} },
		InitialSubs: func() []any {
			return []any{map[string]any{"op": "subscribe", "args": o.subscribeArgs(ctx, o.syms(), subscribed, quarantined)}}
		},
		// provider 符号集变化 → ping tick 差集补订（服务端拒则读报错走重连）
		OnAliveTick: func(s Sender) {
			if args := o.subscribeArgs(ctx, o.syms(), subscribed, quarantined); len(args) > 0 {
				_ = s.Send(map[string]any{"op": "subscribe", "args": args})
			}
		},
		// okx 业务帧=仅 tickers 且走完整 publish 路径（bid/ask 非零产出
		// quote）——ack/channel-conn-count/error/instruments/funding-rate/解析
		// 失败帧全不计（funding 帧持续发射但遮订阅死=审计实证不入集）。
		OnFrame:     func(data []byte) bool { return o.handleMessage(ctx, data, publish) },
		OnReconnect: meter.record,
	})
}

// subscribeArgs 构造订阅 args（D-369 S2：subscribed 集跟踪，ping 期差集增量订阅；
// watchlist 方言归一化 + instruments 目录存在性闸——不存在 instId 跳过不伪造；
// quarantined 进程级隔离表：目录判决对同符号稳定，逐符号只记一行日志）。
func (o *OKXWebSocket) subscribeArgs(ctx context.Context, syms []string, subscribed, quarantined map[string]bool) []map[string]string {
	o.catalog.ensure(ctx)
	var args []map[string]string
	// 包B S6-d：instruments 目录变更推送（SWAP+SPOT 各一 arg）——连接级合成键
	// 走既有差集去重，不随符号增减；重连 subscribed 重置全量重发语义不变。
	for _, instType := range []string{"SWAP", "SPOT"} {
		key := "instruments:" + instType
		if !subscribed[key] {
			subscribed[key] = true
			args = append(args, map[string]string{"channel": "instruments", "instType": instType})
		}
	}
	// armed 交割腿订阅（D-649续④；实现面折 okx_ws_delivery.go——行数门禁）。
	args = append(args, o.deliverySubArgs(ctx, subscribed, quarantined)...)
	for _, sym := range syms {
		spotID, perpID := okxSubCandidates(sym)
		if spotID == "" {
			continue
		}
		spotID, perpID = o.catalog.pick(spotID, perpID)
		if spotID == "" && perpID == "" {
			if o.catalog.loaded() && !quarantined[sym] {
				quarantined[sym] = true
				log.Printf("[okx-ws] %s 不在 okx instruments 目录，跳过订阅", sym)
			}
			continue
		}
		key := spotID + "|" + perpID
		if subscribed[key] {
			continue
		}
		subscribed[key] = true
		if spotID != "" {
			args = append(args, map[string]string{"channel": "tickers", "instId": spotID},
				// bbo-tbt 增订（10-22 tickers ~60-90s 变更驱动节拍病灶——tick-by-tick
				// 最优价；armed/watchlist 小集域帧费受控，tickers 保留同连接托底）。
				map[string]string{"channel": "bbo-tbt", "instId": spotID})
		}
		if perpID != "" {
			args = append(args,
				map[string]string{"channel": "tickers", "instId": perpID},
				map[string]string{"channel": "bbo-tbt", "instId": perpID},
				// funding-rate 公共频道（SWAP 实例）同订阅件，
				// ping 期差集增量覆盖 watchlist 新增。
				map[string]string{"channel": "funding-rate", "instId": perpID},
				// 方案A：mark-price=SWAP 活性脉冲源（前置实证门：XAU-USDT-SWAP
				// 实测 ~10 帧/s 密推，簿面静止期照流——薄品种 est 夹层根治脉冲）。
				map[string]string{"channel": "mark-price", "instId": perpID},
			)
		}
		if spotID != "" {
			// 方案A④：spot 薄品种活性脉冲=index-tickers（同 instId uly 系）。
			args = append(args, map[string]string{"channel": "index-tickers", "instId": spotID})
		}
	}
	return args
}

// handleMessage D-154：返回「本帧是否业务产出帧」（tickers/bbo-tbt bid/ask
// 非零走 publish=true——bbo-tbt 字段同构同径；funding-rate 是 fact 面产出但
// 审计实证不入业务帧集——同帧流会遮订阅死，独立死由 SwapFactMaxAge 兜底）。
func (o *OKXWebSocket) handleMessage(ctx context.Context, data []byte, publish func(bus.Quote)) bool {
	var msg wsMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return false
	}
	if msg.Event == "subscribe" || msg.Event == "channel-conn-count" {
		return false
	}
	// 服务端拒绝帧（error/login 失败等）必须留痕——静默吞帧=僵尸连接难查
	// （实证：订阅被拒后 pong 续期遮读限、零日志断流 18min）。
	if msg.Event == "error" {
		log.Printf("[okx-ws] 服务端 error 帧: %s", data)
		return false
	}
	// funding-rate 帧分流（fundingRate/fundingTime/nextFundingTime 三字段原生）。
	if msg.Arg.Channel == "funding-rate" {
		o.handleFundingRate(ctx, msg)
		return false
	}
	// 方案A②：mark-price/index-tickers=活性脉冲帧——channel 判别处早返
	// （高频帧不走全量解析），只 Touch 脉冲不写报价值；非业务帧=false（脉冲
	// 证明的是本频道活，tickers 独死不被掩——dataIdle 杀仍归其咎）。
	if msg.Arg.Channel == "mark-price" || msg.Arg.Channel == "index-tickers" {
		o.handlePulse(msg)
		return false
	}
	// 包B S6-d：instruments 变更帧 → 逐 instId 失效规格缓存（事件主失效通道）。
	if msg.Arg.Channel == "instruments" {
		o.handleInstruments(msg)
		return false
	}
	if (msg.Arg.Channel != "tickers" && msg.Arg.Channel != "bbo-tbt") || len(msg.Data) == 0 {
		return false
	}

	instID, _ := msg.Arg.InstID.(string)
	if instID == "" {
		return false
	}
	venueSymbol := instID
	isPerp := strings.HasSuffix(instID, "-SWAP")
	venueType := "spot"
	if isPerp {
		venueType = "perp"
		instID = strings.TrimSuffix(instID, "-SWAP")
	}
	canonical := strings.ReplaceAll(instID, "-", "")
	// 交割腿分流：订阅面注入的 instId→canonical 映射命中即 futures 帧——
	// canonical=行情 canonical（'ETHUSDT' 形，与 spot 帧同键；返修③：快照域=
	// q.Symbol 异键永不同帧，发布键钉行情键）。
	if dc, ok := o.delivCanon[venueSymbol]; ok {
		venueType, canonical = "futures", dc
	}

	item := msg.Data[0]
	bidS, _ := item["bidPx"].(string)
	askS, _ := item["askPx"].(string)
	tsS, _ := item["ts"].(string)

	var bid, ask float64
	fmt.Sscanf(bidS, "%f", &bid)
	fmt.Sscanf(askS, "%f", &ask)
	if bid == 0 || ask == 0 {
		return false
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
	return true
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
		Channel  string `json:"channel"`
		InstID   any    `json:"instId"`
		InstType string `json:"instType"` // D-166：私有流 subscribe ack 键分（orders SWAP/SPOT 各证）
	} `json:"arg"`
	Data []map[string]any `json:"data"`
}

// syms D-369 S2：okx WS 订阅符号集（env ∪ provider 动态集，连接期可增量）。
func (o *OKXWebSocket) syms() []string { return o.cfg.effectiveSymbols("okx") }

// SetFactEmit 注入 fact 发射口（wiring 期；先于 Run）。
func (o *OKXWebSocket) SetFactEmit(fn func(ctx context.Context, fs []fact.Fact) error) {
	o.emitFacts = fn
}

// SetPulseFn 注入活性脉冲打点口（D-155 方案A；wiring 期先于 Run；nil 安全）。
func (o *OKXWebSocket) SetPulseFn(fn func(symbol, venueKey string)) { o.pulseFn = fn }

// handleInstruments D-566 包B S6-d：instruments 目录变更帧 → data[].instId 逐个
// 回调由装配层接 exec.OKXSpecInvalidate（一次 log 收敛）；未知字段 WarnUnconsumed 同既有模式。
func (o *OKXWebSocket) handleInstruments(msg wsMsg) {
	instIDs := make([]string, 0, len(msg.Data))
	for _, it := range msg.Data {
		if v, _ := it["instId"].(string); v != "" {
			instIDs = append(instIDs, v)
			if o.OnInstrumentChange != nil {
				o.OnInstrumentChange(v)
			}
		}
	}
	if len(instIDs) > 0 {
		log.Printf("[okx-ws] instruments 变更 %d 条，规格缓存已失效: %v", len(instIDs), instIDs)
	}
	if raw, err := json.Marshal(msg.Data); err == nil {
		WarnUnconsumed("okx", "instruments", raw, okxInstrumentsConsumed)
	}
}

// okxInstrumentsConsumed instruments data[] 已核对字段白名单（设计 §2.6 官方
// 全字段集——D-475b 契约：白名单=已核对清单非仅消费清单，欠列=核对未做）。
var okxInstrumentsConsumed = []string{
	"instId", "instType", "state", "tickSz", "lotSz", "minSz", "maxMktSz",
	"ctVal", "ctValCcy", "ctType", "lever", "listTime", "expTime", "uly",
	"instFamily", "settleCcy", "alias", "baseCcy", "quoteCcy",
}
