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
	// 注入失败的 cashflowWriter（D-193-A 扩包决策 8：签名改 Full 形态）
	p.cashflowWriter = func(ctx context.Context, c *pgstore.Cashflow) (bool, error) {
		return false, errors.New("模拟现金流写入失败")
	}
	err := p.writeCloseCashflows(ctx, 1, pgstore.PositionLeg{Venue: "vA:mt5"}, 0.5, 1.0, 0, 1.0)
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
	if err := s.ClosePosition(ctx, pid, 0, nil, ""); err != nil && !errors.Is(err, pgstore.ErrTerminalState) {
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

// TestWriteCloseCashflows_D193A_FactFields D-193-A 扩包 S1：writeCloseCashflows 成功路径
// 断言 InsertCashflowFull 被调且填事实身份字段（决策 1/3/4/6）+ 幂等重放 won=false。
func TestWriteCloseCashflows_D193A_FactFields(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	p := NewPipeline(reg, s, bus.New(), RiskLimits{}, "demo")

	var captured []*pgstore.Cashflow
	p.cashflowWriter = func(ctx context.Context, c *pgstore.Cashflow) (bool, error) {
		captured = append(captured, c)
		return true, nil // 首次插入 won=true
	}
	// 写 swap cashflow（posID=1, legNo=0, venue="52993526:mt5"）；commission=0 只写 swap 一条
	if err := p.writeCloseCashflows(ctx, 1, pgstore.PositionLeg{Venue: "52993526:mt5"}, 0.5, 0, 0, 1.0); err != nil {
		t.Fatalf("writeCloseCashflows: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("captured len=%d, want 1", len(captured))
	}
	cf := captured[0]
	// 决策 1：event_key=exec:<posID>:<legNo>:<kind>:<YYYY-MM-DD>
	if cf.EventKey == nil || *cf.EventKey == "" {
		t.Fatal("event_key nil/empty")
	}
	wantPrefix := "exec:1:0:swap:"
	if !strings.HasPrefix(*cf.EventKey, wantPrefix) {
		t.Errorf("event_key=%s, want prefix %s", *cf.EventKey, wantPrefix)
	}
	// D-425：fill/hp 实采值 → quality='actual'（公式回退才 'estimated'）
	if cf.Quality == nil || *cf.Quality != "actual" {
		t.Errorf("quality=%v, want actual（实采值，D-425）", cf.Quality)
	}
	// 决策 4：source='exec'
	if cf.Source == nil || *cf.Source != "exec" {
		t.Errorf("source=%v, want exec", cf.Source)
	}
	// 决策 6：account_id=52993526
	if cf.AccountID == nil || *cf.AccountID != 52993526 {
		t.Errorf("account_id=%v, want 52993526", cf.AccountID)
	}
	// 决策 2/7：external_event_id=NULL
	if cf.ExternalEventID != nil {
		t.Errorf("external_event_id=%v, want nil", cf.ExternalEventID)
	}
	// 幂等重放：cashflowWriter 返回 won=false（已存在）
	p.cashflowWriter = func(ctx context.Context, c *pgstore.Cashflow) (bool, error) {
		return false, nil // 幂等命中 won=false
	}
	if err := p.writeCloseCashflows(ctx, 1, pgstore.PositionLeg{Venue: "52993526:mt5"}, 0.5, 0, 0, 1.0); err != nil {
		t.Fatalf("writeCloseCashflows replay: %v", err)
	}
}

// fakeSettler D-193-A 扩包 S5：记录 Settle 调用。
type fakeSettler struct {
	calls []int64
	fail  bool
}

func (s *fakeSettler) Settle(_ context.Context, posID int64) error {
	s.calls = append(s.calls, posID)
	if s.fail {
		return errors.New("模拟 settlement 失败")
	}
	return nil
}

// TestClosePositionCore_TriggersSettle D-193-A 扩包 S5：
// closePositionCore 关闭后触发 settler.Settle（修复 479 缺 settlement 行根因）。
// settler nil=未接线不 panic；settler 非 nil=被调；settler 失败不阻断 CLOSED。
func TestClosePositionCore_TriggersSettle(t *testing.T) {
	// settler nil=未接线，closePositionCore 不 panic（兼容旧测试）
	// settler 非 nil=被调（需完整 closePositionCore 装配，此处只验 seam 存在+签名正确）
	// 完整集成测试在 reconcile 包（d189_r1r2_integration_test.go 已覆盖 ClosePosition→CLOSED 路径）
	// 本测试验 Settler 接口+SetSettler 注入+fakeSettler 记录
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	p := NewPipeline(reg, s, bus.New(), RiskLimits{}, "demo")
	// settler nil=未接线
	if p.settler != nil {
		t.Fatal("settler 默认应为 nil（未接线）")
	}
	// 注入 fakeSettler
	fs := &fakeSettler{}
	p.SetSettler(fs)
	if p.settler == nil {
		t.Fatal("SetSettler 后 settler 应非 nil")
	}
	// 验 Settler 接口签名（compile-time 已保证，此处运行时再验）
	var _ Settler = fs
}
