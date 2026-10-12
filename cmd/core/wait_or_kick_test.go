package main

// D-469a T4/T6：waitOrKick 先到先跑语义 + orderTopic.Publish CEX 事件扇出断言。

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/collect/exchange"
)

// T4：waitOrKick——kick 先到先跑；kick 缺席走定时路径；nil 通道安全当纯定时器。
func TestD469a_WaitOrKick(t *testing.T) {
	ctx := context.Background()

	// kick 支路：长 wait 下 kick 立即返回。
	kickCh := make(chan struct{}, 1)
	kickCh <- struct{}{}
	start := time.Now()
	kicked, done := waitOrKick(ctx, time.Hour, kickCh)
	if !kicked || done || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("kick path: kicked=%v done=%v elapsed=%v", kicked, done, time.Since(start))
	}

	// 定时支路：kick 缺席时短 wait 到期返回 kicked=false。
	kicked, done = waitOrKick(ctx, 20*time.Millisecond, kickCh)
	if kicked || done {
		t.Fatalf("timer path: kicked=%v done=%v", kicked, done)
	}

	// nil 通道=纯定时器（cex-io 入账等待复用）。
	kicked, done = waitOrKick(ctx, 10*time.Millisecond, nil)
	if kicked || done {
		t.Fatalf("nil channel: kicked=%v done=%v", kicked, done)
	}

	// ctx 取消优先。
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, done = waitOrKick(cctx, time.Hour, kickCh); !done {
		t.Fatalf("ctx cancel: done=%v", done)
	}
}

// T4b：kickChanSend 非阻塞——空缓冲送达、满缓冲丢弃不阻塞、nil 通道不阻塞。
func TestD469a_KickChanSend(t *testing.T) {
	ch := make(chan struct{}, 1)
	kickChanSend(ch)
	select {
	case <-ch:
	default:
		t.Fatalf("empty buffered chan should receive")
	}
	kickChanSend(ch) // 已满——不得阻塞/死锁
	kickChanSend(ch)
	kickChanSend(nil)
}

// T6：SetOnPut → orderTopic.Publish{Venue,Ticket:0} 扇出——既有订阅者（engine/bpMon 形态）收到。
func TestD469a_OrderTopicFanout(t *testing.T) {
	topic := bus.NewTopic[bus.OrderEvent]()
	evCh, cancel := topic.Subscribe()
	defer cancel()

	oc := exchange.NewOrderEventCache()
	var bpKicks []string
	// 复刻 main_monitors 装配：Publish+Kick 双叫醒。
	oc.SetOnPut(func(vk string) {
		topic.Publish(bus.OrderEvent{Venue: vk, Ticket: 0})
		bpKicks = append(bpKicks, vk)
	})
	oc.PutOrder("binance:perp", "o9", "", exchange.OrderEvent{State: "filled", At: time.Now()})

	select {
	case ev := <-evCh:
		if ev.Venue != "binance:perp" || ev.Ticket != 0 {
			t.Fatalf("event=%+v want Venue=binance:perp Ticket=0", ev)
		}
	case <-time.After(time.Second):
		t.Fatalf("subscriber did not receive publish")
	}
	if len(bpKicks) != 1 || bpKicks[0] != "binance:perp" {
		t.Fatalf("bpMon kicks=%v", bpKicks)
	}
}
