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
	"arb/internal/safego"
)

// Sink 接收轮询产出，写入存储层。
type Sink func(ctx context.Context, fs []fact.Fact) error

// Named 把注册名、轮询间隔与 Collector 绑定。
type Named struct {
	Name      string
	Interval  time.Duration
	Collector Collector
	// PollTimeout D-398 S1：per-collector 单轮预算（0=回落 Scheduler.PollTimeout→35s）。
	// 长尾扇出采集器（如 okx_funding_history 百级符号串行探测）可放宽而不动全局快速失败语义。
	PollTimeout time.Duration
}

// Scheduler 每源一个 goroutine：独立间隔 + 抖动 + 重试退避。
// 单源故障不影响其他源。Run 阻塞至 ctx 取消。
type Scheduler struct {
	Sources     []Named
	Sink        Sink
	PollTimeout time.Duration

	kickMu sync.Mutex
	kicks  map[string]chan struct{} // name → kick 通道（缓冲 1，D-060 F2）
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
		src := src
		safego.Go("sched:runSource", func() {
			defer wg.Done()
			s.runSource(ctx, src)
		})
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}

func (s *Scheduler) runSource(ctx context.Context, src Named) {
	kick := s.kickChan(src.Name)
	attempt := 0
	for {
		if err := s.pollOnce(ctx, src); err != nil {
			if ctx.Err() != nil {
				return
			}
			attempt++
			wait := backoff(attempt)
			log.Printf("[collect:%s] poll failed (attempt %d): %v, retry in %s", src.Name, attempt, err, wait)
			// 退避 sleep 同样可被 kick 打断（= 提前重试，无害，D-060 F2）。
			if !s.sleep(ctx, kick, wait) {
				return
			}
			continue
		}
		attempt = 0
		wait := nextWait(src.Interval)
		if !s.sleep(ctx, kick, wait) {
			return
		}
	}
}

// Kick 唤醒该源立即补一轮（D-060 F2 boot 竞态修复：动态 venue attach 晚于首轮 → 最长盲窗
// 从整个 Interval 缩到秒级）。非阻塞投递：缓冲 1 满即丢弃多余 kick，绝不阻塞调用方；
// 补轮后照常按 Interval 等待（节奏相位随补轮点平移）。未知源名安全 no-op。
func (s *Scheduler) Kick(name string) {
	ch := s.kickChan(name)
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default: // 已有 pending kick，丢弃
	}
}

// kickChan 返回（必要时创建）该源的 kick 通道；未知源名返回 nil。
func (s *Scheduler) kickChan(name string) chan struct{} {
	known := false
	for _, src := range s.Sources {
		if src.Name == name {
			known = true
			break
		}
	}
	if !known {
		return nil
	}
	s.kickMu.Lock()
	defer s.kickMu.Unlock()
	if s.kicks == nil {
		s.kicks = make(map[string]chan struct{})
	}
	ch, ok := s.kicks[name]
	if !ok {
		ch = make(chan struct{}, 1)
		s.kicks[name] = ch
	}
	return ch
}

// sleep 等待 d 或被 kick/ctx 打断；返回 false = ctx 取消。kick 到来视为等待结束（立即补轮）。
func (s *Scheduler) sleep(ctx context.Context, kick chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-kick:
		return true
	case <-t.C:
		return true
	}
}

func (s *Scheduler) pollOnce(ctx context.Context, src Named) error {
	timeout := src.PollTimeout
	if timeout == 0 {
		timeout = s.PollTimeout
	}
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
