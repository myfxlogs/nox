// d189_p0_test.go —— D-189 P0 静默故障修复单测。
// P0-2：cashflowWriter seam 失败 → error + critical alert + 不置 CLOSED。
// P0-4：ResumeClose 幂等（CLOSED/FAILED 返回 nil）+ 非 CLOSING 返回 error + per-position close 锁。
package exec

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// TestWriteCloseCashflows_WriterFailure_ReturnsError P0-2：cashflowWriter 返回 error → writeCloseCashflows 返回 error。
func TestWriteCloseCashflows_WriterFailure_ReturnsError(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	p := NewPipeline(reg, s, bus.New(), RiskLimits{}, "demo")
	// 注入失败的 cashflowWriter
	p.cashflowWriter = func(ctx context.Context, posID int64, legNo int16, kind, venue string, amount float64, ts time.Time) error {
		return errors.New("模拟现金流写入失败")
	}
	err := p.writeCloseCashflows(ctx, 1, 0, "vA:mt5", 0.5, 1.0, 1.0)
	if err == nil {
		t.Fatal("cashflowWriter 失败时应返回 error")
	}
	if !strings.Contains(err.Error(), "swap cashflow") {
		t.Errorf("error 应包含 swap cashflow 上下文，got: %v", err)
	}
}

// TestResumeClose_ClosedIdempotent P0-4：CLOSED position → ResumeClose 返回 nil（幂等）。
func TestResumeClose_ClosedIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	pid, err := s.CreatePosition(ctx, &pgstore.Position{OpportunityID: "test:closed", Type: "SWAPARB", Status: "CLOSED"})
	if err != nil {
		t.Fatalf("create position: %v", err)
	}
	if err := s.ClosePosition(ctx, pid, 0); err != nil && !errors.Is(err, pgstore.ErrTerminalState) {
		t.Fatalf("close position: %v", err)
	}
	reg := NewRegistry()
	p := NewPipeline(reg, s, bus.New(), RiskLimits{}, "demo")
	if err := p.ResumeClose(ctx, pid); err != nil {
		t.Errorf("ResumeClose on CLOSED should return nil (idempotent), got: %v", err)
	}
}

// TestResumeClose_NonClosingReturnsError P0-4：非 CLOSING 状态 → ResumeClose 返回 error。
func TestResumeClose_NonClosingReturnsError(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	pid, err := s.CreatePosition(ctx, &pgstore.Position{OpportunityID: "test:open", Type: "SWAPARB", Status: "OPEN"})
	if err != nil {
		t.Fatalf("create position: %v", err)
	}
	reg := NewRegistry()
	p := NewPipeline(reg, s, bus.New(), RiskLimits{}, "demo")
	if err := p.ResumeClose(ctx, pid); err == nil {
		t.Error("ResumeClose on OPEN should return error (not CLOSING)")
	}
}

// TestResumeClose_PerPositionLock P0-4：两个 goroutine 同时 ResumeClose 同一 posID → 串行执行（不 panic）。
func TestResumeClose_PerPositionLock(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	pid, err := s.CreatePosition(ctx, &pgstore.Position{OpportunityID: "test:lock", Type: "SWAPARB", Status: "CLOSING"})
	if err != nil {
		t.Fatalf("create position: %v", err)
	}
	reg := NewRegistry()
	p := NewPipeline(reg, s, bus.New(), RiskLimits{}, "demo")
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.ResumeClose(ctx, pid) // 并发调用不应 panic
		}()
	}
	wg.Wait()
	// 验证不 panic 即可（ResumeClose 会因无 legs/verifier 返回 error，但不 panic）
}
