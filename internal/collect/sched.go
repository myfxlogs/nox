package collect

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	"arb/internal/fact"
)

// Sink 接收轮询产出，写入存储层。
type Sink func(ctx context.Context, fs []fact.Fact) error

// Named 把注册名、轮询间隔与 Collector 绑定。
type Named struct {
	Name      string
	Interval  time.Duration
	Collector Collector
}

// Scheduler 每源一个 goroutine：独立间隔 + 抖动 + 重试退避。
// 单源故障不影响其他源。Run 阻塞至 ctx 取消。
type Scheduler struct {
	Sources     []Named
	Sink        Sink
	PollTimeout time.Duration
}

// Run 启动全部轮询源并阻塞直至 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) error {
	if s.Sink == nil {
		return errors.New("collect: scheduler: nil Sink")
	}
	for _, src := range s.Sources {
		if src.Name == "" || src.Collector == nil {
			return fmt.Errorf("collect: scheduler: invalid source %q", src.Name)
		}
	}
	var wg sync.WaitGroup
	for _, src := range s.Sources {
		wg.Add(1)
		go func(src Named) {
			defer wg.Done()
			s.runSource(ctx, src)
		}(src)
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}

func (s *Scheduler) runSource(ctx context.Context, src Named) {
	attempt := 0
	for {
		if err := s.pollOnce(ctx, src); err != nil {
			if ctx.Err() != nil {
				return
			}
			attempt++
			wait := backoff(attempt)
			log.Printf("[collect:%s] poll failed (attempt %d): %v, retry in %s", src.Name, attempt, err, wait)
			if !sleepCtx(ctx, wait) {
				return
			}
			continue
		}
		attempt = 0
		wait := nextWait(src.Interval)
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

func (s *Scheduler) pollOnce(ctx context.Context, src Named) error {
	timeout := s.PollTimeout
	if timeout == 0 {
		timeout = 35 * time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fs, err := src.Collector.Poll(pctx)
	if err != nil {
		return fmt.Errorf("poll: %w", err)
	}
	if err := s.Sink(ctx, fs); err != nil {
		return fmt.Errorf("sink: %w", err)
	}
	return nil
}

func backoff(attempt int) time.Duration {
	return time.Duration(1<<min(attempt, 5)) * time.Second
}

func nextWait(interval time.Duration) time.Duration {
	jitter := 0.9 + 0.2*rand.Float64()
	return time.Duration(float64(interval) * jitter)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
