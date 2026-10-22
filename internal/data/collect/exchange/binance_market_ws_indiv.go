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
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/rejects"
	"nox/internal/platform/safego"
)

// indivRescan 逐符号集重扫节拍（watchlist 差集生效窗）。
const indivRescan = 60 * time.Second

func init() { rejects.Register("binance_indiv", "budget_cap") } // D-663 预算闸拒因键

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
// desired=D-663 执行面集（ExecFaceFn armed∪毕业∪在仓——非宇宙集；
// D-652 饱和定谳=env∪watchlist 灌宇宙 113 连榨干 5Mbps 病灶根治），
// 预算闸=IndivBudgetFn cap 计 desired 集（键缺=indiv 关+告警显式）。
// 60s 差集增删：新符号起连接、移出符号停连接。
func (b *BinanceMarketWS) runIndivBookTickers(ctx context.Context, venueType string, cands []WSCandidate, publish func(bus.Quote)) {
	running := map[string]context.CancelFunc{}
	defer func() {
		for _, cancel := range running {
			cancel()
		}
	}()
	if venueType == "perp" {
		b.ensurePerpInfo(ctx) // 目录一次拉取（fail-open——拉败全量订阅不误杀）
	}
	budgetWarned := false
	for {
		desired, quarantined, over := b.indivDesiredSet(ctx, venueType)
		if over {
			rejects.Inc(rejects.Key{Face: "binance_indiv", Gate: "budget_cap"})
		}
		if (over || indivOff()) && !budgetWarned {
			budgetWarned = true
			if b.cfg.AlertFn != nil {
				b.cfg.AlertFn("warn", indivAlertMsg)
			}
		} else if !over && !indivOff() {
			budgetWarned = false
		}
		if quarantined != indivQuarantined.Load() {
			indivQuarantined.Store(quarantined)
			log.Printf("[binance-indiv] perp 目录隔离 %d 符号（幽灵流连发阻断）", quarantined)
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

// indivAlertMsg/budgetCapState 预算闸态机共享面（单测锚可直读）。
var indivAlertMsg string
var indivOffFlag bool

func indivOff() bool { return indivOffFlag }

// indivDesiredSet desired 集构建（D-663 单测锚——执行面集过滤+预算闸
// 截断三段逻辑出函数体）：face=ExecFaceFn 集（nil fn=空）；perp/spot
// 目录判沿用；cap 超=字母序截断稳定成员；键缺=全空+off 旗。
func (b *BinanceMarketWS) indivDesiredSet(ctx context.Context, venueType string) (desired map[string]bool, quarantined int64, over bool) {
	desired = map[string]bool{}
	if b.cfg.ExecFaceFn == nil {
		return desired, 0, false
	}
	face, err := b.cfg.ExecFaceFn(ctx)
	if err != nil {
		log.Printf("[binance-indiv] 执行面集查询失败 %v（保空集）", err)
		return desired, 0, false
	}
	for stream, vt := range face {
		if vt != venueType {
			continue
		}
		if venueType == "spot" && !b.spotOK(stream) {
			continue
		}
		if venueType == "perp" && !b.perpOK(stream) {
			quarantined++
			continue
		}
		desired[stream] = true
	}
	indivOffFlag = false
	if b.cfg.IndivBudgetFn == nil {
		return desired, quarantined, false
	}
	cap, ok := b.cfg.IndivBudgetFn(ctx)
	if !ok {
		indivOffFlag = true
		indivAlertMsg = "[binance-indiv] 订阅预算键缺失——indiv 面显式关闭"
		return map[string]bool{}, quarantined, false
	}
	if len(desired) > int(cap) {
		indivAlertMsg = fmt.Sprintf("[binance-indiv] %s desired %d 超预算 cap=%d——超出符号不建 indiv（留聚合流覆盖）", venueType, len(desired), cap)
		var syms []string
		for k := range desired {
			syms = append(syms, k)
		}
		sort.Strings(syms)
		desired = map[string]bool{}
		for _, k := range syms[:cap] {
			desired[k] = true
		}
		return desired, quarantined, true
	}
	return desired, quarantined, false
}
