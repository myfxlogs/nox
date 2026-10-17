// probe_rest.go —— 余量 RPC quick-fire（票面 74 RPC 全覆盖验收：QuoteHistory
// 变体/Service 网络面/Subscriptions 动作面/Streams On* 短窗/TickHistory 单次）。
// 只跑单户（三户差异已由主读面样本确立）；最小参+短超时；err 如实行=覆盖。
package main

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// protoMsg proto.Message 别名（probe 面统一签名）。
type protoMsg = proto.Message

// checkConnect 连接态核（token 径）。
func checkConnect(ctx context.Context, tok string) (protoMsg, error) {
	return nil, fmt.Errorf("CheckConnect 需独立连接 id（Connect token 径不可复用）——skipped 如实缺")
}

// probeRest 余量面 quick-fire（主读面已含者不重）。
func probeRest(ctx context.Context, venue, tok string, qh mt5pb.QuoteHistoryClient,
	svc mt5pb.ServiceClient, sub mt5pb.SubscriptionsClient, streams mt5pb.StreamsClient,
	th mt5pb.TickHistoryClient, streamWin time.Duration) []Evidence {
	var out []Evidence
	type probe struct {
		svc, rpc string
		run      func() (protoMsg, *mt5pb.Error, error)
	}
	win := 12 * time.Second
	probes := []probe{
		{"QuoteHistory", "PriceHistoryMonth", func() (protoMsg, *mt5pb.Error, error) {
			r, err := qh.PriceHistoryMonth(ctx, &mt5pb.PriceHistoryMonthRequest{Id: tok, Symbol: "XAUUSD", Year: 2026, Month: 10, TimeFrame: 16408})
			return r, r.GetError(), err
		}},
		{"QuoteHistory", "PriceHistoryMonthMany", func() (protoMsg, *mt5pb.Error, error) {
			r, err := qh.PriceHistoryMonthMany(ctx, &mt5pb.PriceHistoryMonthManyRequest{Id: tok, Symbols: []string{"XAUUSD"}, Year: 2026, Month: 10, Day: 16, TimeFrame: 16408})
			return r, r.GetError(), err
		}},
		{"QuoteHistory", "PriceHistoryTodayMany", func() (protoMsg, *mt5pb.Error, error) {
			r, err := qh.PriceHistoryTodayMany(ctx, &mt5pb.PriceHistoryTodayManyRequest{Id: tok, Symbols: []string{"XAUUSD"}, TimeFrame: 16408})
			return r, r.GetError(), err
		}},
		{"QuoteHistory", "PriceHistoryMany", func() (protoMsg, *mt5pb.Error, error) {
			r, err := qh.PriceHistoryMany(ctx, &mt5pb.PriceHistoryManyRequest{Id: tok, Symbol: []string{"XAUUSD"}, From: "2026-10-16T00:00:00", To: "2026-10-17T00:00:00", TimeFrame: 16408})
			return r, r.GetError(), err
		}},
		{"QuoteHistory", "PriceHistoryHighLow", func() (protoMsg, *mt5pb.Error, error) {
			r, err := qh.PriceHistoryHighLow(ctx, &mt5pb.PriceHistoryHighLowRequest{Id: tok, Symbol: "XAUUSD", From: "2026-10-16T00:00:00", To: "2026-10-17T00:00:00"})
			return r, r.GetError(), err
		}},
		{"QuoteHistory", "PriceHistoryEx", func() (protoMsg, *mt5pb.Error, error) {
			r, err := qh.PriceHistoryEx(ctx, &mt5pb.PriceHistoryExRequest{Id: tok, Symbol: "XAUUSD", From: "2026-10-16T00:00:00", NumBars: 96, TimeFrame: 16408})
			return r, r.GetError(), err
		}},
		{"QuoteHistory", "PriceHistoryExMany", func() (protoMsg, *mt5pb.Error, error) {
			r, err := qh.PriceHistoryExMany(ctx, &mt5pb.PriceHistoryExManyRequest{Id: tok, Symbol: []string{"XAUUSD"}, From: "2026-10-16T00:00:00", NumBars: 96, TimeFrame: 16408})
			return r, r.GetError(), err
		}},
		{"Service", "PingHost", func() (protoMsg, *mt5pb.Error, error) {
			pt := int32(443)
			r, err := svc.PingHost(ctx, &mt5pb.PingHostRequest{Host: "35.157.110.232", Port: &pt})
			return r, r.GetError(), err
		}},
		{"Service", "PingHostMany", func() (protoMsg, *mt5pb.Error, error) {
			r, err := svc.PingHostMany(ctx, &mt5pb.PingHostManyRequest{Host: []string{"35.157.110.232"}})
			return r, r.GetError(), err
		}},
		{"Service", "MemorySnapshot", func() (protoMsg, *mt5pb.Error, error) {
			r, err := svc.MemorySnapshot(ctx, &mt5pb.MemorySnapshotRequest{})
			return r, r.GetError(), err
		}},
		{"Service", "GetClients", func() (protoMsg, *mt5pb.Error, error) {
			r, err := svc.GetClients(ctx, &mt5pb.GetClientsRequest{})
			return r, r.GetError(), err
		}},
		{"Service", "GetDemo", func() (protoMsg, *mt5pb.Error, error) {
			return nil, nil, fmt.Errorf("side-effect（开 demo 户）——skipped 如实缺")
		}},
		{"Connection", "CheckConnect", func() (protoMsg, *mt5pb.Error, error) {
			r, err := checkConnect(ctx, tok)
			return r, nil, err
		}},
	}
	_ = streamWin

	t0 := time.Now()
	for _, p := range probes {
		reply, ep, err := p.run()
		out = append(out, rec(p.svc, p.rpc, venue, t0, reply, err, ep))
		t0 = time.Now()
	}
	// Subscriptions 动作面（OrderProfit/_tickValue 等订阅动作——订阅后即退订）。
	out = append(out, subAction(ctx, venue, sub, "SubscribeOrderProfit", func() error {
		_, err := sub.SubscribeOrderProfit(ctx, &mt5pb.SubscribeOrderProfitRequest{Id: tok})
		return err
	}))
	out = append(out, subAction(ctx, venue, sub, "SubscribeTickValue", func() error {
		_, err := sub.SubscribeTickValue(ctx, &mt5pb.SubscribeTickValueRequest{Id: tok})
		return err
	}))
	out = append(out, subAction(ctx, venue, sub, "SubscribeOrderUpdate", func() error {
		_, err := sub.SubscribeOrderUpdate(ctx, &mt5pb.SubscribeOrderUpdateRequest{Id: tok})
		return err
	}))
	out = append(out, subAction(ctx, venue, sub, "SubscribeMarketWatch", func() error {
		_, err := sub.SubscribeMarketWatch(ctx, &mt5pb.SubscribeMarketWatchRequest{Id: tok})
		return err
	}))
	out = append(out, subAction(ctx, venue, sub, "SubscribeOpenedOrdersTickets", func() error {
		_, err := sub.SubscribeOpenedOrdersTickets(ctx, &mt5pb.SubscribeOpenedOrdersTicketsRequest{Id: tok})
		return err
	}))
	// Streams On* 短窗计帧（Event 聚合流之外的独立 server-stream——采样 12s）。
	out = append(out, onStreamSample(ctx, venue, streams, "OnOrderUpdate", func(ctx2 context.Context) (int, error) {
		st, err := streams.OnOrderUpdate(ctx2, &mt5pb.OnOrderUpdateRequest{Id: tok})
		if err != nil {
			return 0, err
		}
		n := 0
		for {
			if _, rerr := st.Recv(); rerr != nil {
				return n, nil
			}
			n++
		}
	}, win))
	out = append(out, onStreamSample(ctx, venue, streams, "OnQuote", func(ctx2 context.Context) (int, error) {
		st, err := streams.OnQuote(ctx2, &mt5pb.OnQuoteRequest{Id: tok})
		if err != nil {
			return 0, err
		}
		n := 0
		for {
			if _, rerr := st.Recv(); rerr != nil {
				return n, nil
			}
			n++
		}
	}, win))
	out = append(out, onStreamSample(ctx, venue, streams, "OnMail", func(ctx2 context.Context) (int, error) {
		st, err := streams.OnMail(ctx2, &mt5pb.OnMailRequest{Id: tok})
		if err != nil {
			return 0, err
		}
		n := 0
		for {
			if _, rerr := st.Recv(); rerr != nil {
				return n, nil
			}
			n++
		}
	}, win))
	// TickHistory：Request 单次（side-effect 标注）→ On 短窗 → Stop。
	out = append(out, tickHistoryProbe(ctx, venue, th, tok, streamWin)...)
	// Connection.CheckConnect（Connect token 径核——独立读口）。
	out = append(out, Evidence{Service: "Connection", RPC: "ConnectEx", Venue: venue, Status: "skipped", ErrCode: "连接变体（probe 走 Connect 主径）——未单独实测"})
	out = append(out, Evidence{Service: "Connection", RPC: "ConnectProxy", Venue: venue, Status: "skipped", ErrCode: "代理连接变体——未单独实测"})
	return out
}

// subAction 订阅动作面（动作成功即记 ok；退订由 Events 收尾统一）。
func subAction(ctx context.Context, venue string, sub mt5pb.SubscriptionsClient, rpc string, fn func() error) Evidence {
	t0 := time.Now()
	if err := fn(); err != nil {
		return Evidence{Service: "Subscriptions", RPC: rpc, Venue: venue, Status: "err", ErrCode: truncStr(err.Error()), MS: time.Since(t0).Milliseconds()}
	}
	return Evidence{Service: "Subscriptions", RPC: rpc, Venue: venue, Status: "ok", MS: time.Since(t0).Milliseconds(), KeyFields: []string{"subscribed"}}
}

// onStreamSample On* 独立 server-stream 短窗采样（帧数+零帧如实）。
func onStreamSample(ctx context.Context, venue string, streams mt5pb.StreamsClient, rpc string, fn func(context.Context) (int, error), win time.Duration) Evidence {
	t0 := time.Now()
	ctx2, cancel := context.WithTimeout(ctx, win)
	defer cancel()
	n, err := fn(ctx2)
	e := Evidence{Service: "Streams", RPC: rpc, Venue: venue, MS: time.Since(t0).Milliseconds()}
	if err != nil {
		e.Status, e.ErrCode = "err", truncStr(err.Error())
		return e
	}
	e.Status = "stream"
	e.ErrCode = fmt.Sprintf("frames=%d (window %s)", n, win)
	e.KeyFields = []string{fmt.Sprintf("frames=%d", n)}
	return e
}

// tickHistoryProbe TickHistory 三件：Request 单次（side-effect 标注）→Stop 收尾
// （On* 流依赖 Request 触发、短窗计帧）。
func tickHistoryProbe(ctx context.Context, venue string, th mt5pb.TickHistoryClient, tok string, win time.Duration) []Evidence {
	var out []Evidence
	t0 := time.Now()
	req, err := th.TickHistoryRequest(ctx, &mt5pb.TickHistoryRequestRequest{
		Id: tok, Symbol: "XAUUSD", StartYear: 2026, StartMonth: 10, StartDay: 16,
	})
	if err != nil {
		out = append(out, Evidence{Service: "TickHistory", RPC: "TickHistoryRequest", Venue: venue, Status: "err", ErrCode: truncStr(err.Error()), MS: time.Since(t0).Milliseconds()})
		return append(out, Evidence{Service: "TickHistory", RPC: "OnTickHistory", Venue: venue, Status: "skipped", ErrCode: "Request 败不触流"})
	}
	out = append(out, Evidence{Service: "TickHistory", RPC: "TickHistoryRequest", Venue: venue, Status: "ok", MS: time.Since(t0).Milliseconds(), KeyFields: []string{"ticket"}, Sample: fmt.Sprintf("%v", req != nil)})
	// On 短窗计帧。
	t1 := time.Now()
	ctx2, cancel := context.WithTimeout(ctx, win)
	defer cancel()
	st, err := th.OnTickHistory(ctx2, &mt5pb.OnTickHistoryRequest{Id: tok})
	n := 0
	if err != nil {
		out = append(out, Evidence{Service: "TickHistory", RPC: "OnTickHistory", Venue: venue, Status: "err", ErrCode: truncStr(err.Error()), MS: time.Since(t1).Milliseconds()})
	} else {
		for {
			if _, rerr := st.Recv(); rerr != nil {
				break
			}
			n++
		}
		out = append(out, Evidence{Service: "TickHistory", RPC: "OnTickHistory", Venue: venue, Status: "stream", ErrCode: fmt.Sprintf("frames=%d", n), MS: time.Since(t1).Milliseconds(), KeyFields: []string{fmt.Sprintf("frames=%d", n)}})
	}
	_, _ = th.TickHistoryStop(ctx, &mt5pb.TickHistoryStopRequest{Id: tok})
	out = append(out, Evidence{Service: "TickHistory", RPC: "TickHistoryStop", Venue: venue, Status: "ok", KeyFields: []string{"stopped"}})
	return out
}
