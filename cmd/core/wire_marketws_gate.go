// wire_marketws_gate.go —— 行情 WS 停止闸构造件（业主令「全面停止采集探研」）。
// NOX_MARKET_WS_PAUSED_<VENUE>=1 即不起行情 WS——重启生效（热读在构造点；
// 私面监护 gate_private_ws/capitalview 铁边界零动=earn/在仓监护面保）。
package main

import (
	"context"
	"log"
	"strings"

	"nox/internal/data/bus"
	"nox/internal/data/collect/exchange"
	"nox/internal/data/fact"
	"nox/internal/infra/dephealth"
	"nox/internal/platform/safego"
)

func marketWsPaused(pause func(context.Context, string) (bool, error), venue string) bool {
	p, err := pause(context.Background(), venue)
	if err != nil {
		log.Printf("[wire] market ws pause 读败 fail-open 不起闸: %v", err)
		return false
	}
	if p {
		log.Printf("[wire] %s 行情 WS 停闸命中（NOX_MARKET_WS_PAUSED_%s=1）", venue, venue)
		markMarketDormant(venue) // dephealth 休眠标——等待面显「休眠」非「等待」
	}
	return p
}

func newGateWebSocketGated(cfg exchange.Config, pause func(context.Context, string) (bool, error)) *exchange.GateWebSocket {
	if marketWsPaused(pause, "GATE") {
		return nil // 闸停=行情 WS 不起（gate 私面监护另件保）
	}
	return exchange.NewGateWebSocket(cfg)
}

func newBinanceMarketWSGated(cfg exchange.Config, pause func(context.Context, string) (bool, error)) *exchange.BinanceMarketWS {
	if marketWsPaused(pause, "BINANCE") {
		return nil
	}
	return exchange.NewBinanceMarketWS(cfg)
}

// wireGateMarketWS gate 行情 WS 停闸+装配+Run 一件（nil=闸停返回不炸）。
func wireGateMarketWS(ctx context.Context, cfg exchange.Config, pause func(context.Context, string) (bool, error), emitFacts func(context.Context, []fact.Fact) error, qbus *bus.QuoteBus) *exchange.GateWebSocket {
	gateWs := newGateWebSocketGated(cfg, pause)
	if gateWs != nil { // 停闸=nil——行情件不起（私面监护另径保，armed 簿 fail-closed 休眠）
		gateWs.SetFactEmit(emitFacts)
		safego.Go("main:gate-ws", func() { gateWs.Run(ctx, qbus.Publish) })
	}
	return gateWs
}

// wireBinanceMarketWS binance 行情 WS 同闸一件（nil=闸停）。
func wireBinanceMarketWS(ctx context.Context, cfg exchange.Config, pause func(context.Context, string) (bool, error), emitFacts func(context.Context, []fact.Fact) error, intervals *exchange.FundingIntervalCache, qbus *bus.QuoteBus) {
	ws := newBinanceMarketWSGated(cfg, pause)
	if ws != nil {
		ws.SetFactEmit(emitFacts)
		ws.SetFundingIntervals(intervals)
		safego.Go("main:binance-mkt-ws", func() { ws.Run(ctx, qbus.Publish) })
	}
}

// markMarketDormant 停闸 venue 行情面 dephealth 键注册休眠谓词
// （私面监护键铁边界不入：gate:ws-private / binance:*-user 全保）。
func markMarketDormant(venue string) {
	switch venue {
	case "GATE":
		dephealth.Dormant(func(k string) bool { return k == "gate:ws-market" })
	case "BINANCE":
		dephealth.Dormant(func(k string) bool {
			return strings.HasPrefix(k, "binance:ws-") && !strings.HasSuffix(k, "-user")
		})
	}
}
