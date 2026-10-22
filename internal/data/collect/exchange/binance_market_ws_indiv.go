// binance_market_ws_indiv.go —— armed/watchlist 腿逐符号 bookTicker 增订
// （10-22 量子化病灶：!bookTicker 聚合流把逐品种更新压 ~5s 桶——40s 窗
// BTCUSDT 仅 8 帧 gaps 全 5s 网格=venue 量子指纹；armed 腿判定鲜度不足）。
// 一符号一连接=每连接单流唯一存活形（D-531「突发杀」实读=每连接多流杀；
// fstream epoch 概率裁断 28-175s——被杀=常态重连非异常）。聚合流不动=
// 全宇宙面+断窗 ~5s 量子托底。符号集=effectiveSymbols（env∪watchlist∪
// 毕业腿）60s 轮差集增删——watchlist 新增随下轮生效。
package exchange

import (
	"context"
	"strings"
	"sync"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/safego"
)

// indivRescan 逐符号集重扫节拍（watchlist 差集生效窗）。
const indivRescan = 60 * time.Second

// bookTickerStreamSym canonical→bookTicker 流名（normalize 消费面——
// XAGUSDT 死流实证：watchlist canonical 已带计价尾被再拼 "+usdt"=
// xagusdtusdt 无流零帧；已带 USDT/USDC/FDUSD 尾直 lowercase 不再拼，
// 裸基才 +usdt）。主径/ indiv 径同件单源。
func bookTickerStreamSym(sym string) string {
	l := strings.ToLower(sym)
	for _, q := range []string{"usdt", "usdc", "fdusd"} {
		if strings.HasSuffix(l, q) {
			return l
		}
	}
	return l + "usdt"
}

// startIndivBookTickers armed/watchlist 腿逐符号 bookTicker 增订两路扇出
// （10-22 量子化修——聚合流不动=全宇宙托底；一符号一连接=每连接单流唯一
// 存活形，被杀常态重连）。spot 路先 ensureSpotInfo 与主流同款序。
func (b *BinanceMarketWS) startIndivBookTickers(ctx context.Context, publish func(bus.Quote), wg *sync.WaitGroup) {
	safego.GoLoopPlain("binance_market_ws:indiv-perp", func() {
		b.runIndivBookTickers(ctx, "perp", b.publicCands, publish)
		wg.Done()
	})
	safego.GoLoopPlain("binance_market_ws:indiv-spot", func() {
		b.ensureSpotInfo(ctx)
		b.runIndivBookTickers(ctx, "spot", b.spotCands, publish)
		wg.Done()
	})
}

// runIndivBookTickers 逐符号增订扇出（venueType=spot/perp 各一路）：
// desired=effectiveSymbols 周期重扫，新符号起连接、移出符号停连接。
func (b *BinanceMarketWS) runIndivBookTickers(ctx context.Context, venueType string, cands []WSCandidate, publish func(bus.Quote)) {
	running := map[string]context.CancelFunc{}
	defer func() {
		for _, cancel := range running {
			cancel()
		}
	}()
	for {
		desired := map[string]bool{}
		for _, sym := range b.cfg.effectiveSymbols("binance") {
			if venueType == "spot" && !b.spotOK(sym) {
				continue
			}
			desired[bookTickerStreamSym(sym)] = true
		}
		for sym := range running {
			if !desired[sym] {
				running[sym]()
				delete(running, sym)
			}
		}
		for sym := range desired {
			if running[sym] != nil {
				continue
			}
			sctx, cancel := context.WithCancel(ctx)
			running[sym] = cancel
			stream := sym + "@bookTicker"
			sub := make([]WSCandidate, 0, len(cands))
			for _, c := range cands {
				nc := c
				nc.URL = strings.TrimSuffix(c.URL, "/") + "/" + stream // raw-path 单流（无 SUBSCRIBE）
				sub = append(sub, nc)
			}
			vt := venueType
			safego.GoLoopPlain("binance-indiv:"+vt+":"+sym, func() {
				RunWS(sctx, WSSpec{
					Name:        "binance-indiv:" + vt + ":" + sym,
					Candidates:  sub,
					Dialer:      b.dialer,
					Header:      b.header,
					DepKey:      "binance:ws-indiv-" + vt,
					DataIdle:    mktWSDataIdle,
					AlertFn:     b.cfg.AlertFn,
					InitialSubs: func() []any { return nil }, // raw-path 无订阅帧
					OnFrame:     func(data []byte) bool { return b.handleBookTicker(data, vt, publish) },
				})
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(indivRescan):
		}
	}
}
