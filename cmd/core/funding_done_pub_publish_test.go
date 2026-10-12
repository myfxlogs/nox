// d518_funding_kick_test.go —— D-518 R4 测试锚：WS funding emit → factsDone 桥接三件——
// emit 成功才发布（失败透传不发布）、WS src 精确抽取（非 funding 源过滤）、60s 合流节流。
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"nox/internal/data/fact"
)

// publish 收取 fundingDoneCh 当前积压（带超时防死锁），返回收到的 src 集。
func drainFundingDone(t *testing.T, n int) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	for range n {
		select {
		case s := <-fundingDoneCh:
			got[s] = true
		case <-time.After(time.Second):
			t.Fatalf("期待 %d 个发布，实收 %v", n, got)
		}
	}
	return got
}

// assertQuiet 断言通道无发布（50ms 静默窗）。
func assertQuiet(t *testing.T, why string) {
	t.Helper()
	select {
	case s := <-fundingDoneCh:
		t.Fatalf("%s：不得发布，却收到 %q", why, s)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestD518_FundingDonePubPublish(t *testing.T) {
	InitFundingDoneChannel()
	pub := &fundingDonePub{}

	// 首批：distinct WS src 各发一次（批内去重），非 funding src 过滤且不占节流窗。
	pub.publish([]fact.Fact{
		{Kind: fact.KindFunding, Src: "binance_ws_markprice"},
		{Kind: fact.KindFunding, Src: "binance_ws_markprice"}, // 批内去重
		{Kind: fact.KindFunding, Src: "okx_ws_v5"},
		{Kind: fact.KindFunding, Src: "mt_swap"},
	})
	got := drainFundingDone(t, 2)
	if len(got) != 2 || !got["binance_ws_markprice"] || !got["okx_ws_v5"] {
		t.Fatalf("want {binance_ws_markprice,okx_ws_v5}, got %v", got)
	}

	// 二批（<60s）：节流合流抑制。
	pub.publish([]fact.Fact{{Kind: fact.KindFunding, Src: "gate_ws_futures"}})
	assertQuiet(t, "60s 内二批应被抑制")

	// 纯非 funding 批：不发布也不占节流窗。
	pub.publish([]fact.Fact{{Kind: fact.KindFunding, Src: "cex_depth"}})
	assertQuiet(t, "非 funding 批")
}

func TestD518_WrapFundingEmit(t *testing.T) {
	InitFundingDoneChannel()
	pub := &fundingDonePub{}
	fs := []fact.Fact{{Kind: fact.KindFunding, Src: "okx_ws_v5"}}

	// raw 失败：错误透传且不发布（该批未落库≠源完成）。
	rawErr := func(context.Context, []fact.Fact) error { return errors.New("sink down") }
	if err := wrapFundingEmit(rawErr, pub)(context.Background(), fs); err == nil {
		t.Fatal("raw 错误必须透传")
	}
	assertQuiet(t, "emit 失败")

	// raw 成功：本批透传且发布 funding src。
	var gotLen int
	rawOK := func(_ context.Context, got []fact.Fact) error { gotLen = len(got); return nil }
	if err := wrapFundingEmit(rawOK, pub)(context.Background(), fs); err != nil || gotLen != 1 {
		t.Fatalf("raw 成功路径：err=%v gotLen=%d", err, gotLen)
	}
	if got := drainFundingDone(t, 1); !got["okx_ws_v5"] {
		t.Fatalf("emit 成功后应发布 okx_ws_v5，got %v", got)
	}
}

// TestD593_PublishNonBlockingOnFullChannel D-593 T1：channel 满（bridge goroutine
// 死亡等形态）时 publish 必须按时返回——emit 路径永不在热读循环上阻塞下游 channel。
// 止血前形态（裸 send）下本测死锁红；加固后 select-default 绿。
func TestD593_PublishNonBlockingOnFullChannel(t *testing.T) {
	InitFundingDoneChannel()
	// 手工注满 cap=16（不跑 bridge 消费——模拟 bridge 死后的满 channel 态）。
	for i := 0; i < cap(fundingDoneCh); i++ {
		fundingDoneCh <- "filler"
	}
	pub := &fundingDonePub{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pub.publish([]fact.Fact{
			{Kind: fact.KindFunding, Src: "binance_ws_markprice"},
			{Kind: fact.KindFunding, Src: "okx_ws_v5"},
		})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish 在满 channel 上阻塞——emit 路径不得阻塞下游 channel")
	}
	// T2 回归：fresh channel 正常路径不丢（publish 后 src 可达）。
	InitFundingDoneChannel()
	pub2 := &fundingDonePub{}
	pub2.publish([]fact.Fact{{Kind: fact.KindFunding, Src: "gate_ws_futures"}})
	if got := drainFundingDone(t, 1); !got["gate_ws_futures"] {
		t.Fatalf("正常路径应发布 gate_ws_futures，got %v", got)
	}
}
