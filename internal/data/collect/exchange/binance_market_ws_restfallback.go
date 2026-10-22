// binance_market_ws_restfallback.go —— D-109：markPrice/ticker 死流 REST 看门狗兜底。
// venue 侧选择性断供实拍（D-106 部署后双机位：markPrice 全形态 ack 后零帧 ≥1h、
// !ticker@arr 同死、dial 连败 8 次进 dephealth degraded；REST premiumIndex/ticker 全正常）
// → funding/index/vol24h facts 停供。看门狗形态（D-004 例外留痕 decisions.md D-109）：
// dephealth 判定该依赖流 degraded 才低频拉 REST 补观测 facts，流复绿即停零轮询；
// 不产出报价/业务数据（行情面=bookTicker 流，不受本路径影响）。
package exchange

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"nox/internal/data/fact"
	"nox/internal/infra/dephealth"
)

// restClientFor D-109 R1②：按目标 host 收敛 REST client（binance_user_ws restFor
// /spotInfoClient 同款裁定）——币安原生域裸 client（token/代理 CA 勿出域）；
// 非原生（cex-proxy 路由）仍用 cfg.Client（token+自签 CA 同源）。
func (b *BinanceMarketWS) restClientFor(rawURL string) *http.Client {
	if u, err := url.Parse(rawURL); err == nil && isNativeBinance(u.Host) {
		return b.restNative
	}
	return b.cfg.Client
}

// restFallbackEvery 死流窗 REST 拉取周期（验收=断流期 facts 陈旧≤本周期）。
const restFallbackEvery = 60 * time.Second

// restFallbackStaleAfter D-109 R1①：帧年龄闸阈值——now-lastFrame 超龄=死流。
// markPrice@arr@1s/!ticker@arr 理论 1s 一帧，5min 无帧=venue 侧断供实锤。
const restFallbackStaleAfter = 5 * time.Minute

// 死流依赖键（与 runLoop DepKey 构造 "binance:ws-"+venueType+"-"+kind 同源字面量）。
const (
	binanceDepKeyPerpMarkPrice = "binance:ws-perp-markPrice"
	binanceDepKeyPerpTicker    = "binance:ws-perp-ticker"
)

// REST 兜底轨 facts src（与 WS 轨 binance_ws_markprice/binance_ws_ticker 区分，复盘可辨）。
const (
	restFallbackSrcMarkPrice = "binance_rest_markprice"
	restFallbackSrcTicker    = "binance_rest_ticker"
)

// runRestFallback 常驻看门狗：定点醒一次查 dephealth 态，红窗才拉（拍间零缓存零业务数据）。
func (b *BinanceMarketWS) runRestFallback(ctx context.Context) {
	for {
		t := time.NewTimer(restFallbackEvery)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		b.restFallbackRound(ctx)
	}
}

// degraded 死流判定（测试注入点；nil=dephealth.Degraded 生产读口）。
func (b *BinanceMarketWS) degraded(key string) bool {
	if b.degradedFn != nil {
		return b.degradedFn(key)
	}
	return dephealth.Degraded(key)
}

// frameStale D-109 R1①：帧年龄闸——该流末帧距今超 staleAfter=死流。
// dephealth.Degraded 连败 3 次才红，「dial 成功→ack→静默」形态逐次 Mark(true)
// 归零永不到 3；帧年龄同覆 dial 死（无帧）与连上但静默（无帧）两形态。
// last=0=采集器未运行（测试直构造）→ 不视死流，闸由 dephealth 单载。
func (b *BinanceMarketWS) frameStale(last *atomic.Int64) bool {
	t := last.Load()
	return t > 0 && b.now().Sub(time.Unix(0, t)) > restFallbackStaleAfter
}

// restFallbackRound 一拍：markPrice 流死 → premiumIndex 补 funding/index facts；
// ticker 流死 → 24hr 补 vol24h facts。流活=零拉取。emitFacts/匹配 client 未装配=静默跳过。
// 死流=帧超龄 OR dephealth degraded（dial 连败极端窗多一层）。
func (b *BinanceMarketWS) restFallbackRound(ctx context.Context) {
	if b.emitFacts == nil {
		return
	}
	if b.frameStale(&b.lastFrameMarkPrice) || b.degraded(binanceDepKeyPerpMarkPrice) {
		b.fetchPremiumIndex(ctx)
	}
	if b.frameStale(&b.lastFrameTicker) || b.degraded(binanceDepKeyPerpTicker) {
		b.fetchTicker24h(ctx)
	}
}

// fetchPremiumIndex fapi/v1/premiumIndex（全市场）→ funding 三件套+index_price facts
// （emitMarkPriceFact 同构语义：缺关键字段不造/interval 缓存真值年化/index 10min 节流；
// 节流 map 本 goroutine 专属——与 WS 轨 map 分离防跨 goroutine 竞争）。
func (b *BinanceMarketWS) fetchPremiumIndex(ctx context.Context) {
	var rows []struct {
		Symbol          string `json:"symbol"`
		IndexPrice      string `json:"indexPrice"`
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
	}
	url := strings.TrimRight(b.cfg.BinanceBaseURL, "/") + "/fapi/v1/premiumIndex"
	client := b.restClientFor(url) // D-109 R1②：原生域裸 client——X-Proxy-Token 勿出域
	if client == nil {
		return
	}
	if err := getJSONInto(ctx, client, url, &rows); err != nil {
		log.Printf("[binance-mkt-ws] rest fallback premiumIndex: %v", err)
		return
	}
	allowed := b.allowedSymbolSet()
	now := b.now()
	if b.indexEmitREST == nil {
		b.indexEmitREST = make(map[string]time.Time)
	}
	var fs []fact.Fact
	for _, r := range rows {
		if r.Symbol == "" || r.LastFundingRate == "" || !binanceAllowedHit(allowed, r.Symbol) {
			continue
		}
		var rate float64
		if _, err := fmt.Sscanf(r.LastFundingRate, "%f", &rate); err != nil {
			log.Printf("[binance-mkt-ws] rest fallback rate %q dirty: %v", r.LastFundingRate, err)
			continue
		}
		periodsPerDay := fundingPeriodsPerDay(0)
		if b.intervals != nil {
			periodsPerDay = b.intervals.PeriodsPerDay(r.Symbol)
		}
		fs = append(fs, fact.Fact{Kind: fact.KindFunding, Venue: VenueBinance, Symbol: r.Symbol,
			Value: annualizeFunding(rate, periodsPerDay), Unit: fact.UnitPctAnnualized,
			Ts: now, Src: restFallbackSrcMarkPrice})
		if r.NextFundingTime > 0 {
			fs = append(fs,
				fact.Fact{Kind: fact.KindFundingNextSettle, Venue: VenueBinance, Symbol: r.Symbol,
					Value: float64(r.NextFundingTime) / 1000, Unit: fact.UnitUnixSec, Ts: now, Src: restFallbackSrcMarkPrice},
				fact.Fact{Kind: fact.KindFundingInterval, Venue: VenueBinance, Symbol: r.Symbol,
					Value: 86400 / float64(periodsPerDay), Unit: fact.UnitSeconds, Ts: now, Src: restFallbackSrcMarkPrice})
		}
		if r.IndexPrice != "" {
			var idx float64
			if _, err := fmt.Sscanf(r.IndexPrice, "%f", &idx); err == nil && idx > 0 {
				if last, ok := b.indexEmitREST[r.Symbol]; !ok || now.Sub(last) >= indexEmitEvery {
					b.indexEmitREST[r.Symbol] = now
					fs = append(fs, fact.Fact{Kind: fact.KindIndexPrice, Venue: VenueBinance, Symbol: r.Symbol,
						Value: idx, Unit: fact.UnitUSD, Ts: now, Src: restFallbackSrcMarkPrice})
				}
			}
		}
	}
	if len(fs) == 0 {
		return
	}
	if err := b.emitFacts(ctx, fs); err != nil {
		log.Printf("[binance-mkt-ws] rest fallback markprice emit: %v", err)
	}
}

// fetchTicker24h fapi/v1/ticker/24hr（全市场）→ vol24h facts（15min 每符号节流，
// 节流 map 本 goroutine 专属）。
func (b *BinanceMarketWS) fetchTicker24h(ctx context.Context) {
	var rows []struct {
		Symbol      string `json:"symbol"`
		QuoteVolume string `json:"quoteVolume"`
	}
	url := strings.TrimRight(b.cfg.BinanceBaseURL, "/") + "/fapi/v1/ticker/24hr"
	client := b.restClientFor(url)
	if client == nil {
		return
	}
	if err := getJSONInto(ctx, client, url, &rows); err != nil {
		log.Printf("[binance-mkt-ws] rest fallback ticker24h: %v", err)
		return
	}
	allowed := b.allowedSymbolSet()
	now := b.now()
	if b.tickerEmitREST == nil {
		b.tickerEmitREST = make(map[string]time.Time)
	}
	var fs []fact.Fact
	for _, r := range rows {
		if r.Symbol == "" || r.QuoteVolume == "" || !binanceAllowedHit(allowed, r.Symbol) {
			continue
		}
		var vol float64
		if _, err := fmt.Sscanf(r.QuoteVolume, "%f", &vol); err != nil || vol <= 0 {
			continue
		}
		if last, ok := b.tickerEmitREST[r.Symbol]; ok && now.Sub(last) < tickerEmitEvery {
			continue
		}
		b.tickerEmitREST[r.Symbol] = now
		fs = append(fs, fact.Fact{Kind: fact.KindVol24h, Venue: VenueBinance, Symbol: r.Symbol,
			Value: vol, Unit: fact.UnitUSD, Ts: now, Src: restFallbackSrcTicker})
	}
	if len(fs) == 0 {
		return
	}
	if err := b.emitFacts(ctx, fs); err != nil {
		log.Printf("[binance-mkt-ws] rest fallback ticker emit: %v", err)
	}
}

// runTickerPoll D-663 补正①：!ticker@arr 聚合流退役→REST 15min 轮询
// （节拍对齐 facts_retention 既有 15min 节流粒度——零鲜度损失面）。
func (b *BinanceMarketWS) runTickerPoll(ctx context.Context) {
	for {
		b.fetchTicker24h(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Minute):
		}
	}
}
