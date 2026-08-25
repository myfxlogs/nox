package adapter

import (
	"context"
	"log"
	"time"

	mt4pb "arb/internal/adapter/gen/mtapi/mt4"
	"arb/internal/bus"
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
	defer close(ch)
	for {
		if ctx.Err() != nil {
			return
		}
		sctx := a.withSessionMD(ctx)
		a.resubscribeAll(sctx)
		stream, err := a.streams.OnQuote(sctx, &mt4pb.OnQuoteRequest{Id: a.getToken()})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[mt4:%s] onquote stream open: %v", a.venue, err)
			a.quoteReconnectBackoff(ctx)
			continue
		}
		if !a.consumeQuoteFrames(ctx, stream, ch) {
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
		a.quoteReconIdx = 0
	}
}

// quoteReconnectBackoff OnQuote 流重连退避（复用 marginReconnectBackoff，同 MT5）。
func (a *MT4Adapter) quoteReconnectBackoff(ctx context.Context) {
	idx := a.quoteReconIdx
	if idx >= len(marginReconnectBackoff) {
		idx = len(marginReconnectBackoff) - 1
	}
	d := marginReconnectBackoff[idx]
	a.quoteReconIdx++
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}
