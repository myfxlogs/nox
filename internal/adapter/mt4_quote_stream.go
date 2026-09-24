package adapter

import (
	"context"
	"fmt"
	"log"
	"time"

	mt4pb "arb/internal/adapter/gen/mtapi/mt4"
	"arb/internal/bus"
	"arb/internal/safego"
)

// saveSubSet 初始化订阅集镜像（同 MT5）。
func (a *MT4Adapter) saveSubSet(symbols []string) {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	a.subSet = make(map[string]bool, len(symbols))
	for _, s := range symbols {
		a.subSet[s] = true
	}
}

// addSubSet 增补符号到订阅集（同 MT5）。
func (a *MT4Adapter) addSubSet(symbols []string) {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	if a.subSet == nil {
		a.subSet = make(map[string]bool)
	}
	for _, s := range symbols {
		a.subSet[s] = true
	}
}

// snapshotSubSet 返回当前订阅集快照（同 MT5）。
func (a *MT4Adapter) snapshotSubSet() []string {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	out := make([]string, 0, len(a.subSet))
	for s := range a.subSet {
		out = append(out, s)
	}
	return out
}

// resubscribeAll 用当前订阅集全量重订阅（同 MT5，mtapi Subscribe 幂等）。
func (a *MT4Adapter) resubscribeAll(sctx context.Context) {
	for _, sym := range a.snapshotSubSet() {
		if _, err := a.subs.Subscribe(sctx, &mt4pb.SubscribeRequest{
			Id:     a.getToken(),
			Symbol: sym,
		}); err != nil {
			log.Printf("[mt4:%s] resubscribe %s: %v", a.venue, sym, err)
		}
	}
}

// runQuoteStream OnQuote 流消费外层循环（同 MT5 模式）。
func (a *MT4Adapter) runQuoteStream(ctx context.Context, ch chan<- bus.Quote) {
	// D-432：零帧看门狗随 runQuoteStream 生命周期（同 MT5，episode 跨流重连续存）。
	a.zfReset()
	zfDone := make(chan struct{})
	safego.Go("mt4:zeroframeWatchdog", func() { defer close(zfDone); a.zeroframeWatchdog(ctx) })
	defer func() { <-zfDone; close(ch) }()
	for {
		if ctx.Err() != nil {
			return
		}
		sctx, streamCancel := context.WithCancel(a.withSessionMD(ctx))
		a.zfSetCancel(streamCancel)
		a.resubscribeAll(sctx)
		stream, err := a.streams.OnQuote(sctx, &mt4pb.OnQuoteRequest{Id: a.getToken()})
		if err != nil {
			streamCancel()
			a.zfSetCancel(nil)
			if ctx.Err() != nil {
				return
			}
			log.Printf("[mt4:%s] onquote stream open: %v", a.venue, err)
			a.quoteReconnectBackoff(ctx)
			continue
		}
		ok := a.consumeQuoteFrames(ctx, stream, ch)
		streamCancel()
		a.zfSetCancel(nil)
		if !ok {
			return
		}
	}
}

// consumeQuoteFrames 内层 Recv 循环（同 MT5 模式）。
func (a *MT4Adapter) consumeQuoteFrames(ctx context.Context, stream mt4pb.Streams_OnQuoteClient, ch chan<- bus.Quote) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			log.Printf("[mt4:%s] onquote recv: %v", a.venue, err)
			if IsSessionLost(err) {
				if rerr := a.reconnectSession(ctx); rerr != nil {
					log.Printf("[mt4:%s] onquote reconnect: %v", a.venue, rerr)
				}
			}
			a.quoteReconnectBackoff(ctx)
			log.Printf("[mt4:%s] onquote stream reopened", a.venue)
			return true
		}
		q := resp.GetResult()
		// D-432 S1：帧级计数（同 MT5——真帧=symbol 非空且 bid/ask 至少一腿非零）。
		a.zfNoteFrame(q != nil && q.GetSymbol() != "" && (q.GetBid() > 0 || q.GetAsk() > 0))
		if q == nil {
			continue
		}
		brokerSym := q.GetSymbol()
		// 同 mt5.go：实时报价流统一用本地接收时间，规避经纪商服务器时区偏移。
		ts := time.Now()
		ch <- bus.Quote{
			Symbol:      a.canonical(brokerSym),
			Venue:       a.venue,
			VenueSymbol: brokerSym,
			VenueType:   "mt4",
			Bid:         q.GetBid(),
			Ask:         q.GetAsk(),
			Time:        ts,
		}
		a.quoteReconIdx = 0    // 成功消费一帧 → 重置重连退避档位
		a.quoteAlerted = false // D-104 P2 U2：成功帧重置 episode 告警
	}
}

// quoteReconnectBackoff OnQuote 流重连退避（复用 marginReconnectBackoff，同 MT5）。
// D-104 P2 U2：封顶档 episode 告警（去重，成功帧后重置）。
func (a *MT4Adapter) quoteReconnectBackoff(ctx context.Context) {
	idx := a.quoteReconIdx
	capped := idx >= len(marginReconnectBackoff)
	if capped {
		idx = len(marginReconnectBackoff) - 1
		if !a.quoteAlerted && a.AlertSink != nil {
			a.quoteAlerted = true
			a.AlertSink("warn", fmt.Sprintf("mt4:%s quote stream unstable: %d consecutive reconnects", a.venue, a.quoteReconIdx))
		}
	}
	d := marginReconnectBackoff[idx]
	a.quoteReconIdx++
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// --- D-432：OnQuote 零帧死推检测告警（同 MT5 zeroframe watchdog，同构不抽象公共层） ---

// zfReset 看门狗基线初始化（同 MT5）。
func (a *MT4Adapter) zfReset() {
	a.zfMu.Lock()
	defer a.zfMu.Unlock()
	a.zfStartedAt = time.Now()
	a.zfLastAnyFrameAt = time.Time{}
	a.zfLastRealFrameAt = time.Time{}
	a.zfAlerted = false
	a.zfReconnectAt = time.Time{}
	a.zfPersistWarned = false
	a.zfCancelStream = nil
}

// zfSetCancel 登记当前流取消器（同 MT5）。
func (a *MT4Adapter) zfSetCancel(c context.CancelFunc) {
	a.zfMu.Lock()
	defer a.zfMu.Unlock()
	a.zfCancelStream = c
}

// zfNoteFrame 帧级计数（同 MT5）。
func (a *MT4Adapter) zfNoteFrame(real bool) {
	a.zfMu.Lock()
	a.zfLastAnyFrameAt = time.Now()
	var recovered bool
	var hadReconnect bool
	if real {
		a.zfLastRealFrameAt = a.zfLastAnyFrameAt
		recovered = a.zfAlerted || !a.zfReconnectAt.IsZero()
		hadReconnect = !a.zfReconnectAt.IsZero()
		a.zfAlerted = false
		a.zfReconnectAt = time.Time{}
		a.zfPersistWarned = false
	}
	a.zfMu.Unlock()
	if recovered && hadReconnect {
		log.Printf("[mt4:%s] zeroframe recovered after reconnect", a.venue)
	} else if recovered {
		log.Printf("[mt4:%s] zeroframe recovered", a.venue)
	}
}

// zeroframeWatchdog 每 adapter 一条，随 runQuoteStream 生命周期（同 MT5）。
func (a *MT4Adapter) zeroframeWatchdog(ctx context.Context) {
	t := time.NewTicker(zeroFrameCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.zfCheck()
		}
	}
}

// zfCheck 单拍判定（同 MT5；锁内只算状态，sink/cancel/log 出锁执行）。
func (a *MT4Adapter) zfCheck() {
	a.zfMu.Lock()
	now := time.Now()
	var alert string
	var cancel context.CancelFunc
	var persist bool
	if !a.zfLastAnyFrameAt.IsZero() && now.Sub(a.zfLastAnyFrameAt) <= zeroFrameHeartbeatTTL {
		realBase := a.zfLastRealFrameAt
		if realBase.IsZero() {
			realBase = a.zfStartedAt
		}
		if realAge := now.Sub(realBase); realAge > zeroFrameSuspectTTL {
			if !a.zfAlerted {
				a.zfAlerted = true
				alert = fmt.Sprintf("mt4:%s quote stream zeroframe suspect: heartbeats alive, no real frame for %s（终端推送死疑似，KB: mtapi-onquote-zeroframe-terminal-dead）", a.venue, realAge.Round(time.Second))
			}
			if a.zfReconnectAt.IsZero() {
				a.zfReconnectAt = now
				cancel = a.zfCancelStream
			} else if !a.zfPersistWarned && now.Sub(a.zfReconnectAt) > zeroFrameRecoveryTTL {
				a.zfPersistWarned = true
				persist = true
			}
		}
	}
	a.zfMu.Unlock()
	if alert != "" && a.AlertSink != nil {
		a.AlertSink("warn", alert)
	}
	if cancel != nil {
		log.Printf("[mt4:%s] zeroframe stage-2: forcing stream reopen (single shot)", a.venue)
		cancel()
	}
	if persist {
		log.Printf("[mt4:%s] zeroframe persists after reconnect——终端侧死，重连不治本", a.venue)
	}
}
