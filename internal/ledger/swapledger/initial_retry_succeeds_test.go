// d108_initial_retry_test.go —— D-108①锚：swapledger 首轮同日有界重试——失败按注入
// 间隔重试至成功；次数用尽返回末错；零值回落缺省（10min×6 常量锚）。
package swapledger

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// d108RetryStore 复用 nonSettleStubStore 骨架，ListOpenPositionsByType 前 failN 次失败
// （模拟生产首轮 load facts 超时形态——Run 首查即败）。
type d108RetryStore struct {
	nonSettleStubStore
	calls int32
	failN int32
}

func (s *d108RetryStore) ListOpenPositionsByType(_ context.Context, _ []string) ([]pgstore.Position, error) {
	if atomic.AddInt32(&s.calls, 1) <= s.failN {
		return nil, errors.New("load facts: timeout: context deadline exceeded")
	}
	return nil, nil // 零持仓=Run 成功返回
}

// TestD108_InitialRetrySucceeds 前两拍失败第三拍成功 → 恰 3 次 Run、零错误（同日重试闭环）。
func TestD108_InitialRetrySucceeds(t *testing.T) {
	st := &d108RetryStore{failN: 2}
	l := &Ledger{store: st, runTimeout: time.Second,
		initialRetryBackoff: time.Millisecond, initialRetryMax: 5}
	n, err := l.runInitial(context.Background())
	if err != nil {
		t.Fatalf("第三次应成功: %v", err)
	}
	if n != 0 {
		t.Fatalf("零持仓应 0 行: %d", n)
	}
	if st.calls != 3 {
		t.Fatalf("应恰 3 次尝试: %d", st.calls)
	}
}

// TestD108_InitialRetryExhausted 全程失败 → maxN 次后返回末错（当日缺口次日 ticker 兜）。
func TestD108_InitialRetryExhausted(t *testing.T) {
	st := &d108RetryStore{failN: 99}
	l := &Ledger{store: st, runTimeout: time.Second,
		initialRetryBackoff: time.Millisecond, initialRetryMax: 4}
	if _, err := l.runInitial(context.Background()); err == nil {
		t.Fatal("次数用尽应报错")
	}
	if st.calls != 4 {
		t.Fatalf("应恰 maxN 次尝试: %d", st.calls)
	}
}

// TestD108_InitialRetryDefaults 零值回落缺省常量锚（生产 Ledger 未注入即得 10min×6）。
func TestD108_InitialRetryDefaults(t *testing.T) {
	if initialRetryBackoffDefault != 10*time.Minute || initialRetryMaxDefault != 6 {
		t.Fatalf("缺省应为 10min×6: %v %d", initialRetryBackoffDefault, initialRetryMaxDefault)
	}
	// runInitial 零值回落路径：ctx 即刻取消→首次尝试前/后退出不死等 10min。
	l := &Ledger{store: &d108RetryStore{failN: 1}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.runInitial(ctx); err == nil {
		t.Fatal("ctx 取消应带错返回")
	}
}
