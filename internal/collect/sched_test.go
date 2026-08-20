// sched_test.go —— D-060 S2：Scheduler.Kick 立即补轮 / 未知 name no-op / 非阻塞投递。
package collect

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/fact"
)

type kickCollector struct {
	polls atomic.Int32
	ch    chan struct{} // 每次 poll 通知（非阻塞）
}

func (c *kickCollector) Kind() string { return "kick_test" }

func (c *kickCollector) Poll(_ context.Context) ([]fact.Fact, error) {
	c.polls.Add(1)
	select {
	case c.ch <- struct{}{}:
	default:
	}
	return nil, nil
}

func TestSchedulerKickWakesImmediately(t *testing.T) {
	c := &kickCollector{ch: make(chan struct{}, 4)}
	s := &Scheduler{
		Sources: []Named{{Name: "src", Interval: time.Hour, Collector: c}},
		Sink:    func(context.Context, []fact.Fact) error { return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	<-c.ch // 首轮
	s.Kick("src")
	select {
	case <-c.ch: // 补轮立即到（不等 1h Interval）
	case <-time.After(2 * time.Second):
		t.Fatal("kick 未触发立即补轮")
	}

	// 无 kick 不再有轮询（Interval 1h）
	select {
	case <-c.ch:
		t.Fatal("未 kick 不应补轮")
	case <-time.After(200 * time.Millisecond):
	}

	// 未知源名安全 no-op
	s.Kick("nope")
	select {
	case <-c.ch:
		t.Fatal("未知 name 不应触发轮询")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSchedulerKickNonBlocking(t *testing.T) {
	// 源未运行（chan 缓冲 1 满后多余 kick 全丢弃），连续 Kick 不得阻塞调用方。
	c := &kickCollector{ch: make(chan struct{}, 1)}
	s := &Scheduler{
		Sources: []Named{{Name: "a", Interval: time.Hour, Collector: c}},
		Sink:    func(context.Context, []fact.Fact) error { return nil },
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			s.Kick("a")
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Kick 阻塞了（非阻塞投递失效）")
	}
	if n := c.polls.Load(); n != 0 {
		t.Errorf("polls = %d, want 0（未 Run 不轮询）", n)
	}
}
