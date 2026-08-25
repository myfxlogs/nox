package adapter

import (
	"context"
	"fmt"
	"log"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/bus"
)

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

// resubscribeAll 用当前订阅集全量重订阅（OnQuote 流重连后调用，恢复网关侧订阅状态）。
// mtapi Subscribe 幂等，全量重订阅安全。
func (a *MT5Adapter) resubscribeAll(sctx context.Context) {
	for _, sym := range a.snapshotSubSet() {
		if _, err := a.subs.Subscribe(sctx, &mt5pb.SubscribeRequest{
			Id:     a.getToken(),
			Symbol: sym,
		}); err != nil {
			log.Printf("[mt5:%s] resubscribe %s: %v", a.venue, sym, err)
		}
	}
}

// runQuoteStream OnQuote 流消费外层循环：开流 → 消费帧 → 断流退避重开，直到 ctx 取消。
// ch 仅在此 defer close——重连不关 ch，下游 broker_manager_adapters 的 for-range 持续接收。
func (a *MT5Adapter) runQuoteStream(ctx context.Context, ch chan<- bus.Quote) {
	defer close(ch)
	for {
		if ctx.Err() != nil {
			return
		}
		sctx := a.withSessionMD(ctx)
		a.resubscribeAll(sctx)
		stream, err := a.streams.OnQuote(sctx, &mt5pb.OnQuoteRequest{Id: a.getToken()})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[mt5:%s] onquote stream open: %v", a.venue, err)
			a.quoteReconnectBackoff(ctx)
			continue
		}
		if !a.consumeQuoteFrames(ctx, stream, ch) {
			return // ctx 取消
		}
		// 流断 → 外层重开
	}
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
		if q == nil {
			continue
		}
		brokerSym := q.GetSymbol()
		// 实时报价流统一用本地接收时间——mtapi 网关按经纪商服务器时区编码
		// time（FxPro UTC+3 等），各经纪商偏移各异无法通用补偿；本机时钟相对
		// freshness TTL（60s）毫秒级误差可忽略。
		ts := time.Now()
		ch <- bus.Quote{
			Symbol:      a.canonical(brokerSym),
			Venue:       a.venue,
			VenueSymbol: brokerSym,
			VenueType:   "mt5",
			Bid:         q.GetBid(),
			Ask:         q.GetAsk(),
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
		// D-104 P2 U2：封顶档 episode 告警（连续封顶只告警一次，成功帧后 quoteAlerted 重置可再告）。
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
