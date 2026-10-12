package dashboard

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/ledger/reconcile"
	"nox/internal/trading/exec"
)

// slowAcctQuerier 慢速受控查询器（D-460 S1 并发测试）：Account 模拟 200ms 网络 RPC。
type slowAcctQuerier struct {
	key     string
	started chan struct{} // close 信号：RPC 已在飞
	acct    *reconcile.AccountInfo
}

func (s *slowAcctQuerier) VenueKey() string { return s.key }
func (s *slowAcctQuerier) Account(ctx context.Context) (*reconcile.AccountInfo, error) {
	close(s.started)
	select {
	case <-time.After(200 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.acct, nil
}

// TestAccountMonitor_SnapshotNotBlockedByPollRPC：poll 网络段（慢 RPC 200ms）期间
// Snapshot（RLock）不得被写锁阻塞（D-460 S1 验收：断言延迟 <50ms），
// 且 RPC 返回后账户正常合并进视图（离线/合并语义不变）。
func TestAccountMonitor_SnapshotNotBlockedByPollRPC(t *testing.T) {
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	m := NewAccountMonitor(bm, nil)
	slow := &slowAcctQuerier{
		key:     "slow:mt5",
		started: make(chan struct{}),
		acct:    &reconcile.AccountInfo{VenueKey: "slow:mt5", Venue: "slow", VenueType: "mt5", Balance: 100},
	}
	m.AddStatic(slow)

	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		m.poll(context.Background())
	}()

	<-slow.started // 慢 RPC 已在飞

	start := time.Now()
	_ = m.Snapshot()
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("Snapshot 被 poll 阻塞 %v（阈值 50ms）——写锁仍跨 RPC", d)
	}

	select {
	case <-pollDone:
	case <-time.After(5 * time.Second):
		t.Fatal("poll 未在时限内完成")
	}

	view := m.Snapshot()
	if len(view) != 1 || view[0].VenueKey != "slow:mt5" || !view[0].Online {
		t.Fatalf("poll 完成后账户未合并进视图: %+v", view)
	}
}
