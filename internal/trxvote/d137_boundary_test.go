// D-137 测试锚：事件驱动改投——reward 差分 kick / 自发 tx 有界确认窗 / 零奖励 failsafe / boot 补课。
package trxvote

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fbsobreira/gotron-sdk/pkg/proto/core"
)

// d137Fixture 搭一个可完成 doRebalance 的 sched（SR fixture + 账户 fetcher 有值）。
// 返回 (sched, store, cleanup)；doRebalance 完成标记=store.insertedRebalance。
func d137Fixture(t *testing.T, store *fakeStore) (*Scheduler, func()) {
	t.Helper()
	srFixture := `[{"address":"SR1","name":"SR1","annualizedRate":"5.0","brokerage":0,"realTimeVotes":100000,"votes":100000,"producer":true,"producedTotal":100}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(srFixture))
	}))
	SetHTTPClient(srv.Client())
	acc := &mockAccountFetcher{acc: &AccountDetail{Votes: map[string]int64{"SR1": 100}, TronPower: 100}}
	sched, _ := testScheduler(t, store, acc, &testExecutorConn{})
	sched.tronscanBase = srv.URL
	return sched, func() { srv.Close(); SetHTTPClient(&http.Client{}) }
}

// TestD137_BoundaryKick reward 差分 kick → 改投评估触发 + onEvaluated 回调。
func TestD137_BoundaryKick(t *testing.T) {
	store := &fakeStore{}
	sched, cleanup := d137Fixture(t, store)
	defer cleanup()

	evaluated := make(chan struct{}, 4)
	sched.SetOnEvaluated(func(ctx context.Context) { evaluated <- struct{}{} })
	sched.SetFailsafeEvery(time.Hour) // 本锚只测 kick 径，failsafe 不干扰

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.eventLoop(ctx)

	sched.KickBoundary("reward_delta")
	// onEvaluated 在 doRebalance 之后发送——channel 接收即 happens-after 评估完成。
	select {
	case <-evaluated:
	case <-time.After(2 * time.Second):
		t.Fatal("boundary kick did not trigger rebalance eval within 2s")
	}
	if store.insertedRebalance == nil {
		t.Fatal("boundary eval should insert decision record")
	}
}

// TestD137_BoundaryKickCoalesce kick 洪峰合并——chan cap 内排队即消，不阻塞调用方。
func TestD137_BoundaryKickCoalesce(t *testing.T) {
	sched, _ := testScheduler(t, &fakeStore{}, &mockAccountFetcher{}, &testExecutorConn{})
	for i := 0; i < 32; i++ { // cap=4，超出丢弃不阻塞——若阻塞此测试将 hang
		sched.KickBoundary("burst")
	}
}

// TestD137_FailsafeZeroReward 零奖励周期：缓存 nextMaint 已过期+宽限 → failsafe 兜底评估。
func TestD137_FailsafeZeroReward(t *testing.T) {
	store := &fakeStore{}
	sched, cleanup := d137Fixture(t, store)
	defer cleanup()

	nextBoundary := sched.now().Add(2 * time.Hour)
	calls := 0
	sched.SetNextMaintenanceSource(func(ctx context.Context) (time.Time, error) {
		calls++
		return nextBoundary, nil
	})
	// 预填缓存=已过期边界（零奖励周期：reward 差分失聪，缓存边界已过+宽限）
	sched.nextMaint = sched.now().Add(-time.Hour)
	sched.failsafeCheck(context.Background())
	if store.insertedRebalance == nil {
		t.Fatal("failsafe should trigger eval when cached boundary passed + grace")
	}
	if calls == 0 {
		t.Error("failsafe should refresh nextMaintenance after consuming boundary")
	}
}

// TestD137_FailsafeQuietWindow 缓存边界未到期 → failsafe 安静（零外部调用零评估）。
func TestD137_FailsafeQuietWindow(t *testing.T) {
	store := &fakeStore{}
	sched, _ := testScheduler(t, store, &mockAccountFetcher{}, &testExecutorConn{})
	sched.nextMaint = sched.now().Add(3 * time.Hour) // 未来边界
	called := false
	sched.SetNextMaintenanceSource(func(ctx context.Context) (time.Time, error) {
		called = true
		return time.Time{}, nil
	})
	sched.failsafeCheck(context.Background())
	if store.insertedRebalance != nil {
		t.Error("failsafe should not eval before boundary+grace")
	}
	if called {
		t.Error("failsafe should not call external source in quiet window")
	}
}

// TestD137_BootCatchup lastEval < lastBoundary → 启动补评估一次。
func TestD137_BootCatchup(t *testing.T) {
	store := &fakeStore{lastDecidedAt: time.Now().Add(-10 * time.Hour)} // 停机跨过边界
	sched, cleanup := d137Fixture(t, store)
	defer cleanup()
	sched.SetNextMaintenanceSource(func(ctx context.Context) (time.Time, error) {
		return sched.now().Add(time.Hour), nil // lastBoundary=now-5h > lastEval=now-10h
	})
	sched.bootCatchup(context.Background())
	if store.insertedRebalance == nil {
		t.Fatal("boot catch-up should eval once when boundary missed while down")
	}
}

// TestD137_BootCatchupFresh lastEval > lastBoundary → 不补。
func TestD137_BootCatchupFresh(t *testing.T) {
	store := &fakeStore{lastDecidedAt: time.Now().Add(-time.Hour)}
	sched, cleanup := d137Fixture(t, store)
	defer cleanup()
	sched.SetNextMaintenanceSource(func(ctx context.Context) (time.Time, error) {
		return sched.now().Add(time.Hour), nil // lastBoundary=now-5h < lastEval=now-1h
	})
	sched.bootCatchup(context.Background())
	if store.insertedRebalance != nil {
		t.Error("boot catch-up should not eval when lastEval after last boundary")
	}
}

// TestD137_SelfTxConfirmWindow 自发 tx 确认窗：confirmed → onSelfTxConfirmed 触发重取钩子。
func TestD137_SelfTxConfirmWindow(t *testing.T) {
	conn := &testExecutorConn{txInfo: &core.TransactionInfo{Id: []byte("abc")}}
	sched, _ := testScheduler(t, &fakeStore{}, &mockAccountFetcher{}, conn)
	confirmed := make(chan struct{}, 1)
	sched.SetOnSelfTxConfirmed(func(ctx context.Context) { confirmed <- struct{}{} })
	sched.SetConfirmSteps([]time.Duration{time.Millisecond})
	sched.confirmSelfTx(context.Background(), "deadbeef")
	select {
	case <-confirmed:
	case <-time.After(time.Second):
		t.Fatal("confirmed self tx should fire onSelfTxConfirmed")
	}
}

// TestD137_SelfTxConfirmBounded 确认窗有界：3 次回执全未确认 → 放弃不无限等（钩子不触发）。
func TestD137_SelfTxConfirmBounded(t *testing.T) {
	conn := &testExecutorConn{txInfoErr: errors.New("transaction info not found")}
	sched, _ := testScheduler(t, &fakeStore{}, &mockAccountFetcher{}, conn)
	sched.SetConfirmSteps([]time.Duration{time.Millisecond, time.Millisecond, time.Millisecond})
	done := make(chan struct{})
	go func() { sched.confirmSelfTx(context.Background(), "deadbeef"); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("confirm window must be bounded (~3 receipts then give up)")
	}
}

// TestD137_R2_BoundaryConsumed reward-delta 评估后本地滚动消费边界——
// failsafeCheck 紧接着不误补第二轮（R2 退回①）。
func TestD137_R2_BoundaryConsumed(t *testing.T) {
	store := &fakeStore{}
	sched, cleanup := d137Fixture(t, store)
	defer cleanup()
	// 缓存已过期边界（failsafe 视角：已过+宽限）
	sched.nextMaint = sched.now().Add(-time.Hour)
	// 模拟 reward-delta 评估路径（eventLoop boundaryCh 分支同款调用序）
	sched.doRebalance(context.Background())
	sched.noteEvaluated(context.Background())
	if store.insertedID != 1 {
		t.Fatalf("eval should insert 1 record, got %d", store.insertedID)
	}
	// failsafeCheck 立即再查——边界已被本地前滚消费，不得二次评估
	sched.failsafeCheck(context.Background())
	if store.insertedID != 1 {
		t.Fatalf("boundary consumed check: insertedID=%d, want 1（failsafe 误补）", store.insertedID)
	}
}
