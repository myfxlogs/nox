// sched_test.go —— D-060 S2：Scheduler.Kick 立即补轮 / 未知 name no-op / 非阻塞投递。
package collect

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"nox/internal/data/fact"
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

// deadlineCollector D-398 S4①：记录 Poll 收到的 ctx deadline，验证 per-collector 预算注入。
type deadlineCollector struct {
	dl  time.Time
	has bool
}

func (c *deadlineCollector) Kind() string { return "deadline_test" }

func (c *deadlineCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	c.dl, c.has = ctx.Deadline()
	return nil, nil
}

// TestPollOncePerCollectorTimeout D-398 S4①：Named.PollTimeout 覆盖全局；
// 未配置回落 Scheduler.PollTimeout→35s 三级链不变（长尾扇出源放宽不影响其他源）。
func TestPollOncePerCollectorTimeout(t *testing.T) {
	sink := func(context.Context, []fact.Fact) error { return nil }
	s := &Scheduler{PollTimeout: time.Second, Sink: sink}

	c1 := &deadlineCollector{}
	if err := s.pollOnce(context.Background(), Named{Name: "wide", PollTimeout: 10 * time.Minute, Collector: c1}); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if !c1.has || time.Until(c1.dl) < 9*time.Minute {
		t.Fatalf("src.PollTimeout 未生效：deadline=%v has=%v（旧实现红=全局 1s）", c1.dl, c1.has)
	}

	c2 := &deadlineCollector{}
	if err := s.pollOnce(context.Background(), Named{Name: "def", Collector: c2}); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if !c2.has || time.Until(c2.dl) > 2*time.Second {
		t.Fatalf("未配置源应回落 s.PollTimeout=1s：deadline=%v", c2.dl)
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

// TestSchedulerAfterRun D-469c S3：poll+sink 成功后按源名回调；poll 失败不回调。
func TestSchedulerAfterRun(t *testing.T) {
	ok := &kickCollector{ch: make(chan struct{}, 4)}
	fail := &failCollector{}
	var got []string
	s := &Scheduler{
		Sources: []Named{
			{Name: "mt_swap", Interval: time.Hour, Collector: ok},
			{Name: "broken", Interval: time.Hour, Collector: fail},
		},
		Sink:     func(context.Context, []fact.Fact) error { return nil },
		AfterRun: func(name string) { got = append(got, name) },
	}
	if err := s.pollOnce(context.Background(), s.Sources[0]); err != nil {
		t.Fatalf("pollOnce ok: %v", err)
	}
	if len(got) != 1 || got[0] != "mt_swap" {
		t.Fatalf("AfterRun got %v, want [mt_swap]", got)
	}
	if err := s.pollOnce(context.Background(), s.Sources[1]); err == nil {
		t.Fatal("pollOnce fail should return error")
	}
	if len(got) != 1 {
		t.Fatalf("AfterRun fired on failed poll: %v", got)
	}
}

type failCollector struct{}

func (c *failCollector) Kind() string { return "fail_test" }
func (c *failCollector) Poll(context.Context) ([]fact.Fact, error) {
	return nil, context.DeadlineExceeded
}
