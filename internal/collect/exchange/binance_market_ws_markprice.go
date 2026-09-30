// binance_market_ws_markprice.go —— D-582：markPrice 域自 binance_market_ws.go 拆出
// （check-lines <300 先例同 spotinfo/binance_user_ws 拆分）。!markPrice@arr@1s 聚合流
// 数组帧+单对象帧双形解析；单元素处理 emitMarkPriceFact 为原 handleMarkPrice :240-271
// 逻辑搬迁零改（语义不变：缺关键字段不造/venue 禁借用律/emitFacts nil 静默消费）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/fact"
)

// handleMarkPrice D-509 S2：markPriceUpdate 帧→funding 三 fact（rate 年化走
// FundingIntervalCache 真值、T=nextFundingTime ms→NextSettle、interval 同帧发）。
// D-582 帧形分派：!markPrice@arr@1s 聚合流推数组帧（`[` 起首，逐元素过有效集
// 过滤后走 emitMarkPriceFact）；`{` 单对象帧为原逐符号订阅路径（兼容保留）。
// 返回是否已消费（数组帧恒消费不泄漏；非 markPrice 帧/解析失败 → false 交 bookTicker 面）。
func (b *BinanceMarketWS) handleMarkPrice(ctx context.Context, data []byte) bool {
	if len(data) > 0 && data[0] == '[' {
		var arr []map[string]any
		if err := json.Unmarshal(data, &arr); err != nil {
			return false
		}
		// 有效集过滤：聚合流全市场 ~400 符号/1s 不过滤=DB 灌水。s 恒 *USDT 形制，
		// 集内 canonical 与剥 USDT 基名双键命中（与原逐符号订阅集语义等价）。
		allowed := make(map[string]struct{})
		for _, s := range b.cfg.effectiveSymbols("binance") {
			u := strings.ToUpper(s)
			allowed[u] = struct{}{}
			allowed[strings.TrimSuffix(u, "USDT")] = struct{}{}
		}
		for _, m := range arr {
			sym, _ := m["s"].(string)
			u := strings.ToUpper(sym)
			_, hit := allowed[u]
			if !hit {
				_, hit = allowed[strings.TrimSuffix(u, "USDT")]
			}
			if !hit {
				continue
			}
			b.emitMarkPriceFact(ctx, m)
		}
		return true
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	if e, _ := m["e"].(string); e != "markPriceUpdate" {
		return false
	}
	b.emitMarkPriceFact(ctx, m)
	return true
}

// indexEmitEvery index_price per-symbol 心跳节流（D-595 S1：快变值逐帧跳变使
// FactEmitter 值变去重零抑制——54.5 行/s≈470 万行/天实测；10min<radar 读窗 15min 配平）。
const indexEmitEvery = 10 * time.Minute

// emitMarkPriceFact 单元素 markPriceUpdate→funding 三 fact（原单帧逻辑搬迁零改）：
// 缺关键字段不造（venue 禁借用律）；emitFacts nil=静默消费。
func (b *BinanceMarketWS) emitMarkPriceFact(ctx context.Context, m map[string]any) {
	if b.emitFacts == nil {
		return // 是 markPrice 帧但无发射口（测试/未装配）——已消费不泄漏给行情面
	}
	sym, _ := m["s"].(string)
	rateS, _ := m["r"].(string)
	if sym == "" || rateS == "" {
		return // 缺关键字段：该 symbol funding 缺省不造（venue 禁借用律）
	}
	var rate float64
	if _, err := fmt.Sscanf(rateS, "%f", &rate); err != nil {
		log.Printf("[binance-mkt-ws] markPrice funding rate %q dirty: %v", rateS, err)
		return
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
	// D-591 R2：帧内 i=index price 同帧落库；D-595 S1：per-symbol 10min 心跳节流
	// （快变值逐帧跳变使值变去重零抑制，R2「与 funding 同款」对快变值不绑定——
	// D-350 定标教训同型；radar 读窗 15min 配平）。缺/脏=缺省不造，不拖垮 funding 三件套。
	if idxS, _ := m["i"].(string); idxS != "" {
		var idxv float64
		if _, err := fmt.Sscanf(idxS, "%f", &idxv); err == nil && idxv > 0 {
			if last, ok := b.indexEmit[sym]; ok && now.Sub(last) < indexEmitEvery {
				// 10min 窗内跳过（值变也不写）；funding 三件套照常
			} else {
				if b.indexEmit == nil {
					b.indexEmit = make(map[string]time.Time)
				}
				b.indexEmit[sym] = now
				fs = append(fs, fact.Fact{Kind: fact.KindIndexPrice, Venue: VenueBinance, Symbol: sym,
					Value: idxv, Unit: fact.UnitUSD, Ts: now, Src: "binance_ws_markprice"})
			}
		}
	}
	if err := b.emitFacts(ctx, fs); err != nil {
		log.Printf("[binance-mkt-ws] funding emit %s: %v", sym, err)
	}
}
