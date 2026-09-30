// Package reconcile —— StartEventLoop 事件驱动对账循环单测（D-004 P1#6）。
package reconcile

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// countingStore 统计 Run 次数（Run 每轮恰好一次 ListPositions("OPEN")；
// nextBoundary 只查 OPENING/LEGGED/CLOSING，不污染计数）。
type countingStore struct {
	*fakeStore
	runs atomic.Int64
}

func (s *countingStore) ListPositions(ctx context.Context, status string, limit, offset int) ([]pgstore.Position, error) {
	if status == "OPEN" {
		s.runs.Add(1)
	}
	return s.fakeStore.ListPositions(ctx, status, limit, offset)
}

type countingCloser struct{ calls atomic.Int64 }

func (c *countingCloser) AutoClosePosition(context.Context, int64) error {
	c.calls.Add(1)
	return nil
}
func (c *countingCloser) ClaimClose(context.Context, int64) error { return nil }
func (c *countingCloser) ResumeClose(context.Context, int64) error {
	c.calls.Add(1)
	return nil
}

func newEventLoopRecon(s *countingStore, setup func(*Reconciler)) (*Reconciler, context.CancelFunc, chan struct{}) {
	r := New(s)
	r.initialDelay = 30 * time.Millisecond
	if setup != nil {
		setup(r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.StartEventLoop(ctx)
	}()
	return r, cancel, done
}

// (a) 首跑前 kick 不丢：3 次 Kick → 首跑 + pending 补跑 = 共 2 次 Run。
func TestEventLoop_PendingBeforeFirstRun(t *testing.T) {
	s := &countingStore{fakeStore: newFakeStore()}
	r, cancel, done := newEventLoopRecon(s, nil)
	defer func() { cancel(); <-done }()
	r.Kick()
	r.Kick()
	r.Kick()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if s.runs.Load() >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("runs=%d, want 2（首跑+pending 补跑）", s.runs.Load())
}

// (b) 首跑后窗口内 5 次 Kick 合流为 1 次补跑。
func TestEventLoop_KickCoalesce(t *testing.T) {
	s := &countingStore{fakeStore: newFakeStore()}
	r, cancel, done := newEventLoopRecon(s, nil)
	defer func() { cancel(); <-done }()
	waitRuns(t, s, 1, 4*time.Second, "首跑")
	for i := 0; i < 5; i++ {
		r.Kick()
		time.Sleep(50 * time.Millisecond)
	}
	waitRuns(t, s, 2, 4*time.Second, "合流补跑")
	time.Sleep(reconKickCoalesce + 300*time.Millisecond)
	if got := s.runs.Load(); got != 2 {
		t.Fatalf("runs=%d, want 2（窗口内 kick 合流不重复跑）", got)
	}
}

// (c) CLOSING 仓 closing_started_at+30min 到点边界触发 Run + ResumeClose。
func TestEventLoop_ClosingBoundary(t *testing.T) {
	s := &countingStore{fakeStore: newFakeStore()}
	started := time.Now().Add(-closingStuckTimeout + 300*time.Millisecond)
	s.fakeStore.positions[7] = &pgstore.Position{
		ID: 7, Status: "CLOSING", ClosingStartedAt: &started,
	}
	closer := &countingCloser{}
	_, cancel, done := newEventLoopRecon(s, func(r *Reconciler) { r.SetCloser(closer) })
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if closer.calls.Load() >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("ResumeClose 未在到点边界触发（calls=%d runs=%d）", closer.calls.Load(), s.runs.Load())
}

// (d) 无仓 → 边界定时器不触发，Run 计数停在首跑。
func TestEventLoop_NoBoundaryNoRun(t *testing.T) {
	s := &countingStore{fakeStore: newFakeStore()}
	_, cancel, done := newEventLoopRecon(s, nil)
	defer func() { cancel(); <-done }()
	waitRuns(t, s, 1, 4*time.Second, "首跑")
	time.Sleep(2 * time.Second)
	if got := s.runs.Load(); got != 1 {
		t.Fatalf("runs=%d, want 1（无边界不跑）", got)
	}
}

// (e) ctx 取消退出。
func TestEventLoop_Cancel(t *testing.T) {
	s := &countingStore{fakeStore: newFakeStore()}
	_, cancel, done := newEventLoopRecon(s, nil)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 StartEventLoop 未退出")
	}
}

func waitRuns(t *testing.T, s *countingStore, want int64, d time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s.runs.Load() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: runs=%d, want >=%d", what, s.runs.Load(), want)
}
