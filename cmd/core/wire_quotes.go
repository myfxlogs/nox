// wire_quotes.go —— D-366 S2：CEX 报价链装配（自 main.go 拆出，D-366 超线拆分；
// 同构 wire_income/wire_cexscan 先例：纯装配零新逻辑，行为零变更）。
// 5a. CEX 报价 → QuoteBus（OKX/Gate=WS 实时推送；Binance 地理封锁且代理不做 WS 升级 → REST 轮询）。
// 5c. TickSampler（§4.3）——QuoteBus 发布的是完整 pair（sym+"USDT"），订阅符号须一致；
// 采样集数据驱动（D-060 修正①）：MT 侧 canonical 从 broker_symbol_map 派生——旧硬编码三符号
// 清单漏掉 symseed 扩容新 canonical（JPY 交叉/指数/油），违背 F9 前提。
package main

import (
	"context"
	"log"
	"slices"
	"sync"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/collect"
	"nox/internal/data/collect/exchange"
	"nox/internal/data/fact"
	"nox/internal/data/keydomain"
	"nox/internal/platform/alertcool"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
	"nox/internal/trading/engine"
	"nox/internal/trading/exec"
)

// startQuoteStreams CEX 报价三源上总线（OKX/Gate WS + Binance REST 轮询）。
func startQuoteStreams(ctx context.Context, cexCfg exchange.Config, qbus *bus.QuoteBus, alertCooler *alertcool.Cooler, st *pgstore.Store, emitFacts func(ctx context.Context, fs []fact.Fact) error, fundingIntervals *exchange.FundingIntervalCache) *exchange.GateWebSocket {
	// D-507 S2：重连风暴 fail-loud 告警通道注入（同 brokerMgr.AlertSink 先例，
	// main.go alertCooler.Wrap1+st.InsertAlert；测试 nil-safe 静默）。
	// D-104②：canary 红窗内 [ws-storm] 告警聚合进 canary 单条 critical。
	cexCfg.AlertFn = alertCooler.Wrap1(func(level, msg string) {
		if egressAggregated(msg) {
			log.Printf("[egress] canary 红窗聚合告警: %.80s", msg)
			return
		}
		if err := st.InsertAlert(context.Background(), nil, level, msg); err != nil {
			log.Printf("[alert] insert: %v", err)
		}
	})
	// D-101：gate ADL 风险帧持仓关联探针——gate:perp 活跃仓（四态）OPEN 腿符号集
	// 每帧 live 读（帧=事件触发，非轮询）；读败返 true fail-safe（宁多告不漏告）。
	cexCfg.ADLPositionHit = func(venueSymbol string) bool {
		syms, err := st.OpenLegSymbols(ctx, keydomain.Join(exchange.VenueGate, "perp"))
		if err != nil {
			log.Printf("[gate-adl] open leg symbols: %v", err)
			return true
		}
		return slices.Contains(syms, venueSymbol)
	}
	okxWs := exchange.NewOKXWebSocket(cexCfg)
	okxWs.OnInstrumentChange = exec.OKXSpecInvalidate
	okxWs.SetFactEmit(emitFacts)
	okxWs.SetPulseFn(qbus.Touch) // D-155 方案A：mark-price/index-tickers 活性脉冲打点
	safego.GoLoop("main:okx-ws", func() { okxWs.Run(ctx, qbus.Publish) })
	gateWs := exchange.NewGateWebSocket(cexCfg)
	gateWs.SetFactEmit(emitFacts)
	safego.Go("main:gate-ws", func() { gateWs.Run(ctx, qbus.Publish) })
	// D-570 包C S6⑤：返回 ref 供 main_monitors 装配 OnContractInfo（cexStreamMgr 后造——
	// 失效闭包须寻址 collect 侧 ws 实例，构造时不在场）。
	// D-505 S2 + D-508 S14：Binance 公开报价单轨=WS 流（双连接 SUBSCRIBE）；
	// REST 兜底已删（断流→reconnectMeter 告警 + 报价龄期闸 fail_closed 拒新鲜度不足报价）。
	if ws := exchange.NewBinanceMarketWS(cexCfg); ws != nil {
		ws.SetFactEmit(emitFacts)
		ws.SetFundingIntervals(fundingIntervals)
		safego.Go("main:binance-mkt-ws", func() { ws.Run(ctx, qbus.Publish) })
	}
	return gateWs
}

// startTickSampler TickSampler 采样（60s 窗）→ ticks 落库（月分区惰性创建）。
// D-542 S3①：采样对集=有效符号集（env ∪ watchlist ∪ graduation 腿）按 venue 形制——
// SKLUSDT 类毕业腿/watchlist 新币 WS 已推帧却不在采样集=零 tick 入场闸永拒实证。
func startTickSampler(ctx context.Context, qbus *bus.QuoteBus, st *pgstore.Store, cexCfg exchange.Config) {
	cexPairs := tickPairSet(cexCfg)
	allSymbols := append(sampleCanonicals(ctx, st), cexPairs...)
	ticker := bus.NewTickSampler(qbus, 60*time.Second,
		func(ctx context.Context, rows []bus.TickRow) error {
			pgRows := make([]pgstore.TickRow, len(rows))
			for i, r := range rows {
				pgRows[i] = pgstore.TickRow{Ts: r.Ts, Venue: r.Venue, Symbol: r.Symbol, Bid: r.Bid, Ask: r.Ask, VenueType: r.VenueType}
			}
			return st.InsertTicks(ctx, pgRows)
		},
		func(ctx context.Context, y, m int) error { return st.EnsureMonthPartition(ctx, y, m) },
	)
	safego.GoLoop("main:ticker", func() { ticker.Run(ctx, allSymbols) })
}

// tickPairSet 采样对集=三所有效符号集（env ∪ SymProvider）按 venue 对名形制去重展开
// （纯函数，D-542 A4 锚测点）：binance="SKLUSDT"、gate="CL_USDT"、okx="SKL-USDT-SWAP"。
// QuoteBus 订阅键=帧原生 symbol——venue 形制不匹配的对名订而不伤（无帧自然静默）。
func tickPairSet(cexCfg exchange.Config) []string {
	syms := func(venue string) []string {
		out := append([]string{}, cexCfg.Symbols...)
		if cexCfg.SymProvider != nil {
			out = append(out, cexCfg.SymProvider.Symbols(venue)...)
		}
		return out
	}
	seen := map[string]bool{}
	var out []string
	add := func(venue, suffix string) {
		for _, s := range syms(venue) {
			p := s + suffix
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	add("binance", "USDT")
	add("gate", "_USDT")
	add("okx", "-USDT-SWAP")
	return out
}

// newFundingEmitAndIntervals D-509 S1：funding fact 发射器（FactEmitter 去重节流）+
// Binance fundingInfo 周期缓存（boot Populate 一次，周期变化极罕见；失败回退 3/day）。
func newFundingEmitAndIntervals(ctx context.Context, st *pgstore.Store, fc *engine.FactCache, cexCfg exchange.Config) (func(context.Context, []fact.Fact) error, *exchange.FundingIntervalCache) {
	intervals := exchange.NewFundingIntervalCache()
	if err := intervals.Populate(ctx, cexCfg); err != nil {
		log.Printf("[wiring] fundingIntervalCache populate (best-effort, fallback 3/day): %v", err)
	}
	rawEmit := collect.NewFactEmitter(makeMetaAwareSink(st, fc)).Emit
	// D-518 S2：REST 采集器退役后 WS 是唯一 funding fact 入口，emit 成功须发布
	// factsDone 才能触发 wireSwapArbFactsKick（否则 kick 永久沉默）。
	return wrapFundingEmit(rawEmit, &fundingDonePub{}), intervals
}

// wsFundingSrcs D-518 R6 + D-109 修补：funding fact 源名精确集（src 为精确常量，等值匹配
// 禁前缀——防未来 src 命名前缀撞车误触 kick）。三所 WS 原生 + binance 死流窗 REST 兜底源。
// 三处消费同一真源：emit 包装过滤 / pfFundingSourceDone / wireSwapArbFactsKick。
var wsFundingSrcs = map[string]bool{
	"binance_ws_markprice":   true,
	"binance_rest_markprice": true, // D-109 修补：死流窗 REST 兜底 emit 同权触发 kick
	"okx_ws_v5":              true,
	"gate_ws_futures":        true,
}

// fundingDonePub D-518 S2：WS funding emit → factsDone 合流发布器。
// markPrice 3s 节律远密于 5min 轮询，≥60s 合流再发布防 kick 风暴（纯去抖不丢源语义）。
type fundingDonePub struct {
	mu   sync.Mutex
	last time.Time
}

// publish Emit 成功后调用：抽本批 distinct funding WS src，过 60s 闸则逐源入桥。
func (p *fundingDonePub) publish(fs []fact.Fact) {
	srcSet := map[string]bool{}
	for _, f := range fs {
		if wsFundingSrcs[f.Src] {
			srcSet[f.Src] = true
		}
	}
	if len(srcSet) == 0 {
		return
	}
	p.mu.Lock()
	now := time.Now()
	fire := now.Sub(p.last) >= fundingEmitThrottle
	if fire {
		p.last = now
	}
	p.mu.Unlock()
	if !fire {
		return
	}
	// D-593：emit 路径永不在热读循环上阻塞下游 channel——cap 满（bridge goroutine
	// 断线等）即丢 hint 记日志（kick hint 语义允许丢；日志经上方 60s fire 闸天然限流）。
	dropped := 0
	for src := range srcSet {
		select {
		case fundingDoneCh <- src:
		default:
			dropped++
		}
	}
	if dropped > 0 {
		log.Printf("[fundingDone] bridge channel full/uninit: dropped %d src(s)", dropped)
	}
}

// wrapFundingEmit D-518 S2：emit 包装——FactEmitter 成功才经 pub 发布 fundingDone
// （emit 失败=该批未落库，不发完成信号）。
func wrapFundingEmit(raw func(context.Context, []fact.Fact) error, pub *fundingDonePub) func(context.Context, []fact.Fact) error {
	return func(ctx context.Context, fs []fact.Fact) error {
		if err := raw(ctx, fs); err != nil {
			return err
		}
		pub.publish(fs)
		return nil
	}
}

// fundingDoneCh D-518 S2：WS funding emit → factsDone 桥接通道。
// newFundingEmitAndIntervals 在 factsDoneTopic 创建前装配（main 装配序使闭包无法
// 直持 Publish）——通道解耦装配序，wireFundingDoneBridge 回灌。
// D-590 止血：声明即初始化——startQuoteStreams（main:92）早于 wireFundingDoneBridge
// （main:134），其间 funding emit 的 publish 对 nil channel 发送=该 WS 读循环永久楔死
// （生产实证：emitMarkPriceFact→publish 阻塞 chan send(nil chan) 26min，单 boot 只漏一条 fact）。
var fundingDoneCh = make(chan string, 16)

// FundingDoneChannel 返回 funding WS emit 的 factsDone 桥接通道。
func FundingDoneChannel() <-chan string { return fundingDoneCh }

// InitFundingDoneChannel 初始化桥接通道（wireFundingDoneBridge 内调用一次）。
// 无条件重建保测试重置语义；声明态已非 nil，pre-bridge emit 落声明 buffer
// 完成发送不楔死（kick hint 丢弃可接受）。
func InitFundingDoneChannel() { fundingDoneCh = make(chan string, 16) }

// wireFundingDoneBridge D-518 S2：初始化通道并把 fundingDone 回灌 factsDoneTopic
// （kick 谓词以源完成名为触发，桥接后 WS 批次与 REST 时代同语义）。
func wireFundingDoneBridge(factsDone *bus.Topic[string]) {
	InitFundingDoneChannel()
	safego.GoLoop("main:funding-done-bridge", func() {
		for src := range FundingDoneChannel() {
			factsDone.Publish(src)
		}
	})
}

// fundingEmitThrottle D-518 S2：WS funding emit → factsDone 发布最小间隔。
const fundingEmitThrottle = 60 * time.Second
