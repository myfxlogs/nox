package pgstore

// OnPositionChange 回调——成功路径触发一次，失败/终态拦截不触发。

import (
	"context"
	"testing"

	"nox/internal/platform/testutil"
)

func TestD469b_OnPositionChange(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := s.pool.Exec(ctx, "TRUNCATE positions CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var n int
	s.OnPositionChange = func() { n++ }

	id, err := s.CreatePosition(ctx, &Position{Type: "CROSS_VENUE", Status: "OPEN"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if n != 1 {
		t.Fatalf("after create n=%d want 1", n)
	}
	if err := s.UpdatePositionStatus(ctx, id, "OPEN"); err != nil {
		t.Fatalf("update status: %v", err)
	}
	if n != 2 {
		t.Fatalf("after update n=%d want 2", n)
	}
	if err := s.ClosePosition(ctx, id, 1.5, nil, ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	if n != 3 {
		t.Fatalf("after close n=%d want 3", n)
	}
	// 失败路径不触发：重复 ClosePosition → ErrTerminalState，计数不涨。
	if err := s.ClosePosition(ctx, id, 2.0, nil, ""); err != ErrTerminalState {
		t.Fatalf("reclose err=%v want ErrTerminalState", err)
	}
	if n != 3 {
		t.Fatalf("after failed reclose n=%d want 3", n)
	}
	// UpdatePositionStatus 终态拦截同不触发。
	if err := s.UpdatePositionStatus(ctx, id, "OPEN"); err != ErrTerminalState {
		t.Fatalf("update on closed err=%v want ErrTerminalState", err)
	}
	if n != 3 {
		t.Fatalf("after failed update n=%d want 3", n)
	}
}
