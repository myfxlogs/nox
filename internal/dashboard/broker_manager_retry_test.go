// D-209 S2：startWithRetry 测试 + D-208 NULL-case 回归欠账。
package dashboard

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestStartWithRetry_FailsThenSucceeds 失败 2 次后成功 → 断言尝试 3 次+最终成功。
func TestStartWithRetry_FailsThenSucceeds(t *testing.T) {
	origBackoff := retryBackoff
	retryBackoff = func(int) time.Duration { return 1 * time.Millisecond }
	defer func() { retryBackoff = origBackoff }()

	var attempts int32
	startFn := func() error {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			return errFake
		}
		return nil
	}
	m := &BrokerManager{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.startWithRetry(ctx, "vTest", startFn)
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

// TestStartWithRetry_CtxCancelled ctx 取消 → 立即退出不再尝试。
func TestStartWithRetry_CtxCancelled(t *testing.T) {
	origBackoff := retryBackoff
	retryBackoff = func(int) time.Duration { return 50 * time.Millisecond }
	defer func() { retryBackoff = origBackoff }()

	var attempts int32
	startFn := func() error {
		atomic.AddInt32(&attempts, 1)
		return errFake
	}
	ctx, cancel := context.WithCancel(context.Background())
	// 首次失败后，在 backoff 期间取消 ctx
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	m := &BrokerManager{}
	m.startWithRetry(ctx, "vTest", startFn)
	elapsed := time.Since(start)
	// 应在 backoff（50ms）结束前退出
	if elapsed > 100*time.Millisecond {
		t.Errorf("elapsed = %v, want < 100ms (ctx cancel should break backoff)", elapsed)
	}
	if got := atomic.LoadInt32(&attempts); got > 2 {
		t.Errorf("attempts = %d, want <= 2 (ctx cancelled should stop retry)", got)
	}
}

// TestStartWithRetry_BackoffCaps 退避封顶 60s（覆写 backoff 验证封顶逻辑）。
func TestStartWithRetry_BackoffCaps(t *testing.T) {
	origBackoff := retryBackoff
	defer func() { retryBackoff = origBackoff }()

	// 验证默认 retryBackoff 封顶 60s
	for attempt := 0; attempt < 10; attempt++ {
		d := retryBackoff(attempt)
		if d > 60*time.Second {
			t.Errorf("attempt %d: backoff = %v, want <= 60s", attempt, d)
		}
	}
	// attempt=0 → 1s, attempt=1 → 2s, attempt=6 → 64s 封顶 60s
	if d := retryBackoff(0); d != 1*time.Second {
		t.Errorf("attempt 0: backoff = %v, want 1s", d)
	}
	if d := retryBackoff(1); d != 2*time.Second {
		t.Errorf("attempt 1: backoff = %v, want 2s", d)
	}
	if d := retryBackoff(6); d != 60*time.Second {
		t.Errorf("attempt 6: backoff = %v, want 60s (capped)", d)
	}
}

var errFake = fakeErr{}

type fakeErr struct{}

func (fakeErr) Error() string { return "fake connect error" }
