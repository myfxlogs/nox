package adapter

import (
	"context"
	"fmt"
	"log"
	"time"

	mt5pb "arb/internal/data/adapter/gen/mtapi/mt5"
	"arb/internal/data/bus"
	"arb/internal/platform/safego"
)

// resubscribeAll 逐符号超时上限。
var resubscribeCallTimeout = 5 * time.Second

// saveSubSet 初始化订阅集镜像（Subscribe 时调用，覆盖写）。
func (a *MT5Adapter) saveSubSet(symbols []string) {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	a.subSet = make(map[string]bool, len(symbols))
	for _, s := range symbols {
		a.subSet[s] = true
	}
}

// addSubSet 增补符号到订阅集（Resubscribe 时调用，append）。
func (a *MT5Adapter) addSubSet(symbols []string) {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	if a.subSet == nil {
		a.subSet = make(map[string]bool)
	}
	for _, s := range symbols {
		a.subSet[s] = true
	}
}

// snapshotSubSet 返回当前订阅集快照（流重连 resubscribeAll 用，避免持锁调 RPC）。
func (a *MT5Adapter) snapshotSubSet() []string {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	out := make([]string, 0, len(a.subSet))
	for s := range a.subSet {
		out = append(out, s)
	}
	return out
}

// resubscribeAll 全量重订阅（开流后异步执行+逐符号超时挡楔死，D-563 S5；幂等安全）。
func (a *MT5Adapter) resubscribeAll(sctx context.Context) {
	for _, sym := range a.snapshotSubSet() {
		sc, cancel := context.WithTimeout(sctx, resubscribeCallTimeout) // 逐符号超时
		_, err := a.subs.Subscribe(sc, &mt5pb.SubscribeRequest{
			Id:     a.getToken(),
			Symbol: sym,
		})
		cancel()
		if err != nil {
			log.Printf("[mt5:%s] resubscribe %s: %v", a.venue, sym, err)
		}
	}
}

// runQuoteStream 外层循环：开流 → 消费帧 → 断流退避重开直到 ctx 取消；
// ch 仅在此 defer close——重连不关 ch，下游 for-range 持续接收。
func (a *MT5Adapter) runQuoteStream(ctx context.Context, ch chan<- bus.Quote) {
	// 看门狗随 runQuoteStream 生命周期（episode 跨重连续存）；
	// zfDone 先于 close(ch) 收口（T6 不泄漏语义由此保证）。
	a.zfReset()
	zfDone := make(chan struct{})
	safego.GoLoop("mt5:zeroframeWatchdog", func() { a.zeroframeWatchdog(ctx); close(zfDone) }) // D-115：尾部非 defer——panic 重启不预关 zfDone（二次 close=panic）
	defer func() { <-zfDone; close(ch) }()
	for {
		if ctx.Err() != nil {
			return
		}
		sctx, streamCancel := context.WithCancel(a.withSessionMD(ctx))
		a.zfSetCancel(streamCancel)
		stream, err := a.streams.OnQuote(sctx, &mt5pb.OnQuoteRequest{Id: a.getToken()})
		if err != nil {
			streamCancel()
			a.zfSetCancel(nil)
			if ctx.Err() != nil {
				return
			}
			log.Printf("[mt5:%s] onquote stream open: %v", a.venue, err)
			a.quoteReconnectBackoff(ctx)
			continue
		}
		// 重订阅挪开流后异步执行——订阅楔形不再阻塞流建立（逐符号失败只记日志）。
		safego.Go("mt5:resubscribe", func() { a.resubscribeAll(sctx) })
		ok := a.consumeQuoteFrames(ctx, stream, ch)
		streamCancel()
		a.zfSetCancel(nil)
		if !ok {
			return // ctx 取消
		}
		// 流断 → 外层重开
	}
}

// publishSymbol D-560 S1：emitBlocked 命中（多行组非 primary/零 primary 歧义组成员）
// → 降格 brokerSym 原生键发布；否则 canonical（D-383「native 照发」：tick 不丢只禁占槽）。
func publishSymbol(brokerSym, canon string, blocked map[string]bool) string {
	if blocked[brokerSym] {
		return brokerSym
	}
	return canon
}

// consumeQuoteFrames 内层 Recv 循环。返回 false = ctx 取消（外层退出）；true = 流断需重连。
// 流断时若 IsSessionLost → 复用 S1 reconnectSession 重建 account 会话（margin 流同模式）。
func (a *MT5Adapter) consumeQuoteFrames(ctx context.Context, stream mt5pb.Streams_OnQuoteClient, ch chan<- bus.Quote) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			log.Printf("[mt5:%s] onquote recv: %v", a.venue, err)
			if IsSessionLost(err) {
				if rerr := a.reconnectSession(ctx); rerr != nil {
					log.Printf("[mt5:%s] onquote reconnect: %v", a.venue, rerr)
				}
			}
			a.quoteReconnectBackoff(ctx)
			log.Printf("[mt5:%s] onquote stream reopened", a.venue)
			return true
		}
		q := resp.GetResult()
		// 帧级计数——symbol 非空且 bid/ask 一腿非零才算真实报价帧（心跳/空帧不算）。
		a.zfNoteFrame(q != nil && q.GetSymbol() != "" && (q.GetBid() > 0 || q.GetAsk() > 0))
		if q == nil {
			continue
		}
		brokerSym := q.GetSymbol()
		// 实时报价流统一用本地接收时间——mtapi 网关按经纪商服务器时区编码
		// time（FxPro UTC+3 等），各经纪商偏移各异无法通用补偿；本机时钟相对
		// freshness TTL（60s）毫秒级误差可忽略。
		ts := time.Now()
		// 帧内携价缓存——emitPlan 判定之前按原生键写入（publishPositions
		// 同帧取价）；存原始 bid/ask（**不乘 price_scale**——帧携价=venue 原生价，
		// scale 是 D-594 发布面归一，帧内 PnL/现价同钟须原生口径）。
		a.lastQuote.Store(brokerSym, bus.Quote{Symbol: brokerSym, Venue: a.venue,
			VenueSymbol: brokerSym, VenueType: "mt5", Bid: q.GetBid(), Ask: q.GetAsk(), Time: ts})
		// emitPlan 合一决策——canonical 发射键 + price_scale 乘价 +
		// 敏感未标定行降 native/丢弃（identity 兜底原生键=canonical 名时）。
		pubSym, pscale, emit := a.emitPlan(brokerSym)
		if !emit {
			continue
		}
		bid, ask := q.GetBid(), q.GetAsk()
		if pscale != 1.0 {
			bid *= pscale
			ask *= pscale
		}
		ch <- bus.Quote{
			// 报价槽 primary 钉住——emitBlocked 变体/歧义组成员降格
			// 原生键发布，canonical 槽恒 primary（last-write-wins 占槽拒单根因）。
			Symbol:      pubSym,
			Venue:       a.venue,
			VenueSymbol: brokerSym,
			VenueType:   "mt5",
			Bid:         bid,
			Ask:         ask,
			Time:        ts,
		}
		a.quoteReconIdx = 0    // 成功消费一帧 → 重置重连退避档位
		a.quoteAlerted = false // D-104 P2 U2：成功帧重置 episode 告警
	}
}

// quoteReconnectBackoff OnQuote 流重连退避（复用 marginReconnectBackoff 1s/5s/30s，同因同退避）。
// 封顶后保持末档；ctx 取消立即返回。D-104 P2 U2：封顶档 episode 告警（去重，成功帧后重置）。
func (a *MT5Adapter) quoteReconnectBackoff(ctx context.Context) {
	idx := a.quoteReconIdx
	capped := idx >= len(marginReconnectBackoff)
	if capped {
		idx = len(marginReconnectBackoff) - 1
		// U2：封顶档 episode 告警（连续封顶只告警一次，成功帧后 quoteAlerted 重置可再告）。
		if !a.quoteAlerted && a.AlertSink != nil {
			a.quoteAlerted = true
			a.AlertSink("warn", fmt.Sprintf("mt5:%s quote stream unstable: %d consecutive reconnects", a.venue, a.quoteReconIdx))
		}
	}
	d := marginReconnectBackoff[idx]
	a.quoteReconIdx++
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}
