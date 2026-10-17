// probe_streams.go —— 流类 60s 窗计帧（Subscribe+Events 聚合流为主窗——
// On* 独立 server-stream 各开 15s 短窗采样；零帧也如实记）。
package main

import (
	"context"
	"fmt"
	"time"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// probeStreams 流类证据（事件流帧数+首帧 schema 摘录）。
func probeStreams(ctx context.Context, venue, tok string, sub mt5pb.SubscriptionsClient, streams mt5pb.StreamsClient, window time.Duration) []Evidence {
	var out []Evidence
	// 订阅一个流动符号（Events 聚合流的入料）。
	if _, err := sub.Subscribe(ctx, &mt5pb.SubscribeRequest{Id: tok, Symbol: "XAUUSD"}); err != nil {
		out = append(out, Evidence{Service: "Subscriptions", RPC: "Subscribe", Venue: venue, Status: "err", ErrCode: truncStr(err.Error())})
		return out
	}
	out = append(out, Evidence{Service: "Subscriptions", RPC: "Subscribe", Venue: venue, Status: "ok", KeyFields: []string{"symbol"}})

	// Events 聚合流：60s 窗计帧。
	ctx2, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	st, err := streams.Events(ctx2, &mt5pb.EventsRequest{Id: tok})
	frames := 0
	firstSample := ""
	if err != nil {
		out = append(out, Evidence{Service: "Streams", RPC: "Events", Venue: venue, Status: "err", ErrCode: truncStr(err.Error()), MS: window.Milliseconds()})
	} else {
		t0 := time.Now()
		for {
			ev, rerr := st.Recv()
			if rerr != nil {
				break
			}
			if frames == 0 {
				firstSample = truncStr(ev.GetResult())
			}
			frames++
			if time.Since(t0) >= window {
				break
			}
		}
		stt := "stream"
		if frames == 0 {
			stt = "ok" // 零帧如实（窗内无事件非故障——空仓户常态）
		}
		out = append(out, Evidence{Service: "Streams", RPC: "Events", Venue: venue, Status: stt,
			ErrCode: fmt.Sprintf("frames=%d first=%s", frames, firstSample), MS: time.Since(t0).Milliseconds(),
			KeyFields: []string{fmt.Sprintf("frames=%d", frames)}})
	}
	// 退订收尾。
	_, _ = sub.UnSubscribe(ctx, &mt5pb.UnSubscribeRequest{Id: tok, Symbol: "XAUUSD"})
	return out
}
