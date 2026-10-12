// d407_inflight_test.go —— D-407 S1/S3③：ResumeClose 单飞 try-lock。
// 持锁中并发调用 → ErrCloseInFlight 哨兵快速失败（快试/看护/手动三路收敛点）。
package exec

import (
	"context"
	"errors"
	"testing"

	"arb/internal/data/bus"
	"arb/internal/store/pgstore"
)

// TestResumeClose_InFlightSentinel D-407：tryLock 持锁中 → ResumeClose 返 ErrCloseInFlight；
// 释放后恢复可进（可重入）。锁挂 ResumeClose 入口=三调用方（快试驱动/30min 看护/
// 手动 RPC service.go:256）统一收敛。
func TestResumeClose_InFlightSentinel(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	pid, err := s.CreatePosition(ctx, &pgstore.Position{OpportunityID: "test:inflight", Type: "SWAPARB", Status: "CLOSING"})
	if err != nil {
		t.Fatalf("create position: %v", err)
	}
	p := NewPipeline(NewRegistry(), s, bus.New(), RiskLimits{}, "demo")
	unlock, ok := p.tryLockClose(pid)
	if !ok {
		t.Fatal("首次 tryLock 应成功")
	}
	if err := p.ResumeClose(ctx, pid); !errors.Is(err, ErrCloseInFlight) {
		t.Errorf("持锁中 ResumeClose 应返 ErrCloseInFlight, got %v", err)
	}
	unlock()
	if err := p.ResumeClose(ctx, pid); errors.Is(err, ErrCloseInFlight) {
		t.Error("释放后应恢复可进（不应再返 ErrCloseInFlight）")
	}
}
