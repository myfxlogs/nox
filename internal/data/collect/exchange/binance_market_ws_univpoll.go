// binance_market_ws_univpoll.go —— D-665：binance 宇宙聚合面收窄 REST 拍。
// !bookTicker 聚合 ~190KB/s（spot~75+perp~112）=最肥面——逐消费方鲜度举证：
// 发现面（radar 小时读/term_basis/monitor 簿）全 ≥15min 判据级 → REST
// bookTicker 15min 拍替代 ws 宇宙流；REST 返回全市场列表=覆盖率不降；
// 执行面 1s 鲜度=indiv 腿 ws 全保（面不动）。
package exchange

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/data/bus"
)

// univPollEvery D-665：宇宙面 REST 拍周期（paper_mtm_tick_max_age=5min 闸对齐）。
const univPollEvery = 3 * time.Minute

// runUniversePoll binance 宇宙 bookTicker REST 轮询（perp=fapi 全市场、
// spot=api 全市场——两拍各行 15min，REST 行集=原 ws 全符号覆盖率不降）。
func (b *BinanceMarketWS) runUniversePoll(ctx context.Context, publish func(bus.Quote)) {
	perpURL := strings.TrimRight(b.cfg.BinanceBaseURL, "/") + "/fapi/v1/ticker/bookTicker"
	spotBase := "https://data-api.binance.vision"
	if u := b.spotInfoURL; u != "" {
		if i := strings.Index(u, "/api/"); i > 0 {
			spotBase = u[:i]
		}
	}
	spotURL := spotBase + "/api/v3/ticker/bookTicker"
	for {
		b.univPollOne(ctx, perpURL, "perp", publish)
		b.univPollOne(ctx, spotURL, "spot", publish)
		select {
		case <-ctx.Done():
			return
		case <-time.After(univPollEvery):
		}
	}
}

// univPollOne 单端点拍一行集（fail-soft——单拍败记日志下拍再来）。
func (b *BinanceMarketWS) univPollOne(ctx context.Context, url, venueType string, publish func(bus.Quote)) {
	var rows []struct {
		Symbol   string `json:"symbol"`
		BidPrice string `json:"bidPrice"`
		AskPrice string `json:"askPrice"`
		Ts       int64  `json:"time"`
	}
	client := b.restClientFor(url)
	if client == nil {
		return
	}
	if err := getJSONInto(ctx, client, url, &rows); err != nil {
		log.Printf("[binance-mkt-ws] universe poll %s: %v", venueType, err)
		return
	}
	for _, r := range rows {
		if r.Symbol == "" {
			continue
		}
		var bid, ask float64
		if _, err := fmt.Sscanf(r.BidPrice, "%f", &bid); err != nil || bid == 0 {
			continue
		}
		if _, err := fmt.Sscanf(r.AskPrice, "%f", &ask); err != nil || ask == 0 {
			continue
		}
		ts := b.now()
		if r.Ts > 0 {
			ts = time.UnixMilli(r.Ts)
		}
		publish(bus.Quote{Symbol: r.Symbol, Venue: VenueBinance, VenueSymbol: r.Symbol,
			Bid: bid, Ask: ask, Time: ts, VenueType: venueType})
	}
}
