// binance_market_ws_ticker.go D-591 S6：!ticker@arr 24hrTicker 域自 binance_market_ws.go
// 拆出（check-lines ≤300 同 markprice/spotinfo 拆分模式）——vol24h facts 落库+节流。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"arb/internal/fact"
)

// tickerEmitEvery vol24h fact 每符号发射节流（radar 小时级读取，15min 新鲜度足够；
// 全市场 ~400 符号 × 4/h ≈1.6k 行/h，防灌水闸）。
const tickerEmitEvery = 15 * time.Minute

// handleTicker D-591 S6：24hrTicker 数组帧→vol24h facts（每符号 15min 节流；
// 仅 ticker 连接 goroutine 调用，map 无锁）。缺字段/脏值跳过；ack 已分流。
// D-154：返回是否本类帧（数组解析成功=本连接业务帧——节流内跳过仍计消费，
// 「帧属于本流」≠「帧产出 fact」）；解析失败=false。
func (b *BinanceMarketWS) handleTicker(ctx context.Context, data []byte) bool {
	var arr []struct {
		Symbol      string `json:"s"`
		QuoteVolume string `json:"q"`
	}
	if err := json.Unmarshal(data, &arr); err != nil {
		return false
	}
	if b.emitFacts == nil {
		return true
	}
	if b.tickerEmit == nil {
		b.tickerEmit = make(map[string]time.Time)
	}
	now := b.now()
	var fs []fact.Fact
	for _, it := range arr {
		if it.Symbol == "" || it.QuoteVolume == "" {
			continue
		}
		if last, ok := b.tickerEmit[it.Symbol]; ok && now.Sub(last) < tickerEmitEvery {
			continue
		}
		var vol float64
		if _, err := fmt.Sscanf(it.QuoteVolume, "%f", &vol); err != nil || vol <= 0 {
			continue
		}
		b.tickerEmit[it.Symbol] = now
		fs = append(fs, fact.Fact{Kind: fact.KindVol24h, Venue: VenueBinance, Symbol: it.Symbol,
			Value: vol, Unit: fact.UnitUSD, Ts: now, Src: "binance_ws_ticker"})
	}
	if len(fs) == 0 {
		return true
	}
	if err := b.emitFacts(ctx, fs); err != nil {
		log.Printf("[binance-mkt-ws] ticker emit: %v", err)
	}
	return true
}
