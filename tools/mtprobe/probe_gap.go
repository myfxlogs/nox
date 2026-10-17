// probe_gap.go —— 覆盖缺口补测（第一档漏项：many 变体/单查/On* 流四件/订阅 many）。
package main

import (
	"context"
	"time"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// probeGap 缺口补测（主读面样本已确立三户差异，本轮单户）。
func probeGap(ctx context.Context, venue, tok string, mt5 mt5pb.MT5Client,
	sub mt5pb.SubscriptionsClient, streams mt5pb.StreamsClient,
	th mt5pb.TickHistoryClient, win time.Duration) []Evidence {
	var out []Evidence
	call := func(svc, rpc string, fn func() (protoMsg, *mt5pb.Error, error)) {
		t0 := time.Now()
		reply, ep, err := fn()
		out = append(out, rec(svc, rpc, venue, t0, reply, err, ep))
	}
	sym := "XAUUSD"
	call("MT5", "IsTradeSessionMany", func() (protoMsg, *mt5pb.Error, error) {
		r, err := mt5.IsTradeSessionMany(ctx, &mt5pb.IsTradeSessionManyRequest{Id: tok, Symbols: []string{sym}})
		return r, r.GetError(), err
	})
	call("MT5", "IsQuoteSessionMany", func() (protoMsg, *mt5pb.Error, error) {
		r, err := mt5.IsQuoteSessionMany(ctx, &mt5pb.IsQuoteSessionManyRequest{Id: tok, Symbols: []string{sym}})
		return r, r.GetError(), err
	})
	call("MT5", "SymbolSessionsEx", func() (protoMsg, *mt5pb.Error, error) {
		r, err := mt5.SymbolSessionsEx(ctx, &mt5pb.SymbolSessionsExRequest{Id: tok, Symbol: sym})
		return r, r.GetError(), err
	})
	call("MT5", "SymbolSessionsExMany", func() (protoMsg, *mt5pb.Error, error) {
		r, err := mt5.SymbolSessionsExMany(ctx, &mt5pb.SymbolSessionsExManyRequest{Id: tok, Symbol: []string{sym}})
		return r, r.GetError(), err
	})
	call("MT5", "TickValueWithSize", func() (protoMsg, *mt5pb.Error, error) {
		r, err := mt5.TickValueWithSize(ctx, &mt5pb.TickValueWithSizeRequest{Id: tok, Symbol: []string{sym}})
		return r, r.GetError(), err
	})
	call("MT5", "OpenedOrder", func() (protoMsg, *mt5pb.Error, error) {
		tk := int64(0)
		r, err := mt5.OpenedOrder(ctx, &mt5pb.OpenedOrderRequest{Id: tok, Ticket: &tk})
		return r, r.GetError(), err
	})
	call("Subscriptions", "SubscribeMany", func() (protoMsg, *mt5pb.Error, error) {
		r, err := sub.SubscribeMany(ctx, &mt5pb.SubscribeManyRequest{Id: tok, Symbols: []string{sym}})
		return r, r.GetError(), err
	})
	call("Subscriptions", "UnSubscribeMany", func() (protoMsg, *mt5pb.Error, error) {
		r, err := sub.UnSubscribeMany(ctx, &mt5pb.UnSubscribeManyRequest{Id: tok, Symbols: []string{sym}})
		return r, r.GetError(), err
	})
	onStream := func(rpc string, fn func(context.Context) (int, error)) {
		t0 := time.Now()
		ctx2, cancel := context.WithTimeout(ctx, win)
		defer cancel()
		n, err := fn(ctx2)
		e := Evidence{Service: "Streams", RPC: rpc, Venue: venue, MS: time.Since(t0).Milliseconds()}
		if err != nil {
			e.Status, e.ErrCode = "err", truncStr(err.Error())
		} else {
			e.Status, e.ErrCode, e.KeyFields = "stream", "frames="+itoa(n), []string{"frames=" + itoa(n)}
		}
		out = append(out, e)
	}
	onStream("OnTickValue", func(c context.Context) (int, error) {
		st, err := streams.OnTickValue(c, &mt5pb.OnTickValueRequest{Id: tok})
		if err != nil {
			return 0, err
		}
		n := 0
		for {
			if _, e := st.Recv(); e != nil {
				return n, nil
			}
			n++
		}
	})
	onStream("OnOrderProfit", func(c context.Context) (int, error) {
		st, err := streams.OnOrderProfit(c, &mt5pb.OnOrderProfitRequest{Id: tok})
		if err != nil {
			return 0, err
		}
		n := 0
		for {
			if _, e := st.Recv(); e != nil {
				return n, nil
			}
			n++
		}
	})
	onStream("OnMarketWatch", func(c context.Context) (int, error) {
		st, err := streams.OnMarketWatch(c, &mt5pb.OnMarketWatchRequest{Id: tok})
		if err != nil {
			return 0, err
		}
		n := 0
		for {
			if _, e := st.Recv(); e != nil {
				return n, nil
			}
			n++
		}
	})
	onStream("OnOpenedOrdersTickets", func(c context.Context) (int, error) {
		st, err := streams.OnOpenedOrdersTickets(c, &mt5pb.OnOpenedOrdersTicketsRequest{Id: tok})
		if err != nil {
			return 0, err
		}
		n := 0
		for {
			if _, e := st.Recv(); e != nil {
				return n, nil
			}
			n++
		}
	})
	_ = th
	_ = win
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
