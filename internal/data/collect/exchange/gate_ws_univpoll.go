// gate_ws_univpoll.go —— D-665：gate 宇宙面收窄 REST 拍（okx 同律）。
// ws 收窄执行面后，发现面全谱走 REST tickers 15min 轮询——spot/futures
// 两端点行集=原 ws pair 集覆盖率不降。
package exchange

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
)

// gateUniversePollEvery D-665：REST 宇宙拍周期（paper_mtm_tick_max_age=5min 闸对齐）。
const gateUniversePollEvery = 3 * time.Minute

// runGateTickerPoll gate 宇宙 tickers REST 轮询（spot+futures 全谱）。
// gate tickers 返回 best_bid/best_ask（spot）/mark_price 无 b/a——futures
// 用 mark_price 近似（gateFuturesUpdate 同语义面）。
func (g *GateWebSocket) runGateTickerPoll(ctx context.Context, publish func(bus.Quote)) {
	client := g.cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	base := strings.TrimRight(g.cfg.GateBaseURL, "/")
	if base == "" {
		base = "https://api.gateio.ws"
	}
	for {
		// spot：currency_pair/last/bid_ask_spread 家族——REST tickers 返回
		// lowest_ask/highest_bid 键名。
		var spotRows []struct {
			Pair string `json:"currency_pair"`
			Ask  string `json:"lowest_ask"`
			Bid  string `json:"highest_bid"`
		}
		if err := getJSONInto(ctx, client, base+"/api/v4/spot/tickers", &spotRows); err != nil {
			log.Printf("[gate-ws] universe ticker spot: %v", err)
		} else {
			for _, r := range spotRows {
				var bid, ask float64
				if _, err := fmt.Sscanf(r.Bid, "%f", &bid); err != nil || bid == 0 {
					continue
				}
				if _, err := fmt.Sscanf(r.Ask, "%f", &ask); err != nil || ask == 0 {
					continue
				}
				publish(bus.Quote{Symbol: canonicalFromGate(r.Pair), Venue: VenueGate,
					VenueSymbol: r.Pair, Bid: bid, Ask: ask, Time: time.Now(), VenueType: "spot"})
			}
		}
		var futRows []struct {
			Contract         string  `json:"contract"`
			Ask              string  `json:"lowest_ask"`
			Bid              string  `json:"highest_bid"`
			MarkPx           string  `json:"mark_price"`
			FundingRate      string  `json:"funding_rate"`
			FundingNextApply float64 `json:"funding_next_apply"` // unix s（D-509 ws 帧同义映射）
			FundingInterval  float64 `json:"funding_interval"`   // s
		}
		if err := getJSONInto(ctx, client, base+"/api/v4/futures/usdt/tickers", &futRows); err != nil {
			log.Printf("[gate-ws] universe ticker futures: %v", err)
		} else {
			for _, r := range futRows {
				var bid, ask float64
				fmt.Sscanf(r.Bid, "%f", &bid)
				fmt.Sscanf(r.Ask, "%f", &ask)
				if bid == 0 || ask == 0 { // 无盘口档=mark_price 近似（ws 面同语义）
					fmt.Sscanf(r.MarkPx, "%f", &bid)
					ask = bid
				}
				if bid == 0 {
					continue
				}
				canonical := canonicalFromGate(r.Contract)
				ts := time.Now()
				publish(bus.Quote{Symbol: canonical, Venue: VenueGate,
					VenueSymbol: r.Contract, Bid: bid, Ask: ask, Time: ts, VenueType: "perp"})
				// D-665 返修⑤：funding 三字段经 g.emitFacts 发射——REST 拍行
				// 消费面唯一供给（ws tickers 收窄执行面后非执行面断供实证；
				// 镜像 gate_ws_futures.go 发射径语义——缺字段=缺省不造，
				// FactEmitter 去重天然兼容 exec 符号双供）。
				if g.emitFacts != nil && r.FundingRate != "" {
					var rate float64
					if _, err := fmt.Sscanf(r.FundingRate, "%f", &rate); err != nil {
						log.Printf("[gate-ws] funding rate %q dirty: %v", r.FundingRate, err)
						continue
					}
					fs := []fact.Fact{
						{Kind: fact.KindFunding, Venue: VenueGate, Symbol: canonical,
							Value: annualizeFunding(rate, fundingPeriodsPerDay(r.FundingInterval*1000)),
							Unit:  fact.UnitPctAnnualized, Ts: ts, Src: "gate_rest_futures"},
					}
					if r.FundingNextApply > 0 {
						fs = append(fs, fact.Fact{Kind: fact.KindFundingNextSettle,
							Venue: VenueGate, Symbol: canonical, Value: r.FundingNextApply,
							Unit: fact.UnitUnixSec, Ts: ts, Src: "gate_rest_futures"})
					}
					if err := g.emitFacts(ctx, fs); err != nil {
						log.Printf("[gate-ws] universe funding emit %s: %v", r.Contract, err)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(gateUniversePollEvery):
		}
	}
}
