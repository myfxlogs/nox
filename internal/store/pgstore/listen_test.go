// listen_test.go —— D-004 P1#3：Store.Listen 端到端（TestDSN 门控）+ 退避序列单测。
package pgstore

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil"
)

// 端到端：LISTEN 成功兜底调用 → DisableStrategy/EnableStrategy 触发 NOTIFY →
// 2s 内 onEvent 收到；ctx 取消 → Listen 返回。覆盖进程内写路径（触发器对
// 进程外写者同样生效——psql/resettle 走同一触发器，不另测）。
func TestStore_Listen(t *testing.T) {
	s, err := New(context.Background(), testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM strategy_stats WHERE strategy='T_LISTEN'`); err != nil {
		t.Fatalf("cleanup pre: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM strategy_stats WHERE strategy='T_LISTEN'`)
	})

	events := make(chan struct{}, 16)
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		s.Listen(lctx, "strategy_stats", func() { events <- struct{}{} })
		close(done)
	}()

	wait := func(what string) {
		t.Helper()
		select {
		case <-events:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s 2s 内未收到通知", what)
		}
	}
	wait("LISTEN 成功兜底调用")

	if err := s.DisableStrategy(ctx, "T_LISTEN", "test"); err != nil {
		t.Fatalf("DisableStrategy: %v", err)
	}
	wait("DisableStrategy INSERT")
	if err := s.EnableStrategy(ctx, "T_LISTEN"); err != nil {
		t.Fatalf("EnableStrategy: %v", err)
	}
	wait("EnableStrategy UPDATE")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel 后 Listen 未在 2s 内返回")
	}
}

// 退避序列：1s 起翻倍，30s 封顶。
func TestListenBackoff(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second}
	// 32s 超封顶 → 30s；封顶后保持 30s。
	want[5] = 30 * time.Second
	cur := time.Duration(0)
	for i, w := range want {
		cur = listenBackoff(cur)
		if cur != w {
			t.Fatalf("step %d: got %v, want %v", i, cur, w)
		}
	}
	if got := listenBackoff(cur); got != 30*time.Second {
		t.Fatalf("封顶后应保持 30s, got %v", got)
	}
}
