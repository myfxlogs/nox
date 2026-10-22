// binance_market_ws_runloop.go —— BinanceMarketWS.runLoop 拆件（棘轮基线：
// binance_market_ws.go 超 300 行移出）。单连接重连循环——D-511 S4：骨架收编
// RunWS，本函数只装配协议钩子（分批 SUBSCRIBE 帧集/ack 分流/markPrice→bookTicker
// 分发/显式 token 头）。
// kind=连接流类别（bookTicker|markPrice），venueType 恒喂 QuoteBus
// perp/spot——连接身份与 venue 语义分离（venueType 污染=QuoteBus 键断供）。
// perp bookTicker 订阅单帧 "!bookTicker" 聚合流（全市场同事件格式直转下游）。
package exchange

import (
	"context"
	"time"

	"nox/internal/data/bus"
	"nox/internal/infra/dephealth"
)

func (b *BinanceMarketWS) runLoop(ctx context.Context, cands []WSCandidate, venueType, kind string, publish func(bus.Quote), dataIdle time.Duration) {
	meter := newReconnectMeter(venueType+":"+kind+":"+cands[0].URL, b.cfg.AlertFn) // D-507 S2 扩：同款风暴告警
	RunWS(ctx, WSSpec{
		Name: "binance-mkt-ws:" + venueType + ":" + kind, Candidates: cands, DepKey: "binance:ws-" + venueType + "-" + kind, // DepKey D-579 dephealth 记账键
		DataIdle: dataIdle,      // 票名未注 D#：mkt-ws 阈降档票——perp 1s 频谱族 3min 判死盲窗（venue epoch 裁断第四袭实证——10min 阈白送盲窗）；0=回退 quoteWSDataIdle（spot 低频阈不动）
		AlertFn:  b.cfg.AlertFn, // D-074 ①：冷却循环升级「通道死」告警同管道
		InitialSubs: func() []any {
			var params []string
			if venueType == "perp" && kind == "bookTicker" {
				// E1：binance 上游杀招针对逐符号订阅突发——聚合流单订阅存活。
				params = []string{"!bookTicker"}
			} else if venueType == "perp" && kind == "ticker" {
				// 24hrTicker 全市场聚合流（radar 宇宙枚举全量所需，不过滤——
				// 资格判定须覆盖未监控新上市符号；灌水由每符号 15min 节流控制）。
				// 复审实拍：futures 无 "!ticker@arr@1s" 变体（ack 后零帧，@1s 仅 markPrice
				// 有），流名=文档 All Market Tickers "!ticker@arr"（1000ms）。
				params = []string{"!ticker@arr"}
			} else if venueType == "perp" && kind == "markPrice" {
				// markPrice 逐符号订阅同中 D-535 杀招（上游按连接 epoch 概率性
				// 裁断，wscloseprobe 同参数集复现 frames=1 后 1006 被杀）——改全市场
				// 聚合流单订阅（数组帧与单帧元素同构，handleMarkPrice 双形解析+有效集过滤；
				// 流名字面量禁规范化——binance-ws-stream-name-case-sensitive）。
				// 10-19 直探实锤：!markPrice@arr@1s = ack{result:null} 收讫+零帧
				// （venue 停推变体），!markPrice@arr 同窗复帧——降 @1s 变体。
				params = []string{"!markPrice@arr"}
			} else {
				// 符号源与原 ticker 同源（effectiveSymbols：env ∪ SymProvider
				// 动态 watchlist）；watchlist 新增符号随下次重连订阅生效，60s REST 兜底补窗。
				syms := b.cfg.effectiveSymbols("binance")
				params = make([]string, 0, len(syms))
				for _, sym := range syms {
					if venueType == "spot" && !b.spotOK(sym) {
						continue // D-551 S2：现货不存在符号不订（白占上游突发宽度）
					}
					// 按 kind 出订阅参数——bookTicker→public 路由、markPrice→market
					// 路由（funding 是 perp 语义，spot 连接恒 bookTicker 不进此分支）。
					// kind 不 lowercase：新端点流名大小写敏感（实证 btcusdt@markprice
					// /bookticker 全小写=ack 成功+零帧静默断流，与旧端点退役同形态）。
					params = append(params, bookTickerStreamSym(sym)+"@"+kind)
				}
			}
			// merge 面修复：单帧 SUBSCRIBE 超服务端解析上限（实拍 1008 +
			// "Invalid JSON …column 4096"）→ 分批多帧订阅（RunWS 逐帧发送语义已兼容）。
			var frames []any
			for i, id := 0, 1; i < len(params); i += binanceMktSubChunk {
				end := min(i+binanceMktSubChunk, len(params))
				frames = append(frames, map[string]any{
					"method": "SUBSCRIBE", "params": params[i:end], "id": id,
				})
				id++
			}
			return frames
		},
		// binance 业务帧=被消费的本类帧——ticker 连接=24hrTicker 帧解析成
		// 功（vol24h fact 面）；markPrice 连接=markPriceUpdate 帧（fallback 到
		// bookTicker 产出 quote 亦计）；bookTicker 连接=bookTicker 解析产 quote。
		// subscribe ack/解析失败帧不计业务帧（遮订阅死病灶同 okx）。
		OnFrame: func(data []byte) bool {
			if isSubscribeAck(data) {
				return false // {result:null,id:1} ack；{result:…,error} 已在 isSubscribeAck 记日志
			}
			// D-109 R1③：帧到喂 dephealth（节流件内建 1/min/键）——
			// 「连接活帧死」形态 lastEventUnix 前移可证，解禁判据机读达标。
			dephealth.MarkFrameOK("binance:ws-" + venueType + "-" + kind)
			if kind == "ticker" {
				b.lastFrameTicker.Store(b.now().UnixNano()) // D-109 R1①：帧到=流活（含解析失败帧——有流量即非死流）
				// 24hrTicker 数组帧→vol24h facts（D-591 S6 handler 自创建日起
				// 零调用者，OnFrame 缺 ticker 分流=帧落 handleBookTicker 被静默丢弃；
				// ticker 连接不订 bookTicker，全帧归 handleTicker，解析失败静默）。
				return b.handleTicker(ctx, data)
			}
			if kind == "markPrice" {
				b.lastFrameMarkPrice.Store(b.now().UnixNano()) // D-109 R1①
			}
			if kind == "markPrice" && b.handleMarkPrice(ctx, data) {
				return true // markPriceUpdate 帧已消费（market 连接先 markPrice 后 bookTicker 兜底）
			}
			return b.handleBookTicker(data, venueType, publish)
		},
		OnReconnect: meter.record,
	})
}

// binanceMktSubChunk SUBSCRIBE 单帧参数上限（服务端单帧解析上限约 4KB，实拍 column 4096 截断）。
const binanceMktSubChunk = 120
