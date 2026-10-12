// d189_r1r2_integration_test.go —— D-189 R1 T5：auto_close 端到端测试用真实 Pipeline（非 fakeCloser）。
// 验证 auto_close step c（ClaimClose → ResumeClose）与真实 Pipeline.ResumeClose 状态检查衔接：
// position OPEN → ClaimClose（OPEN→CLOSING）→ ResumeClose（CLOSING→closePositionCore→CLOSED）。
//go:build integration

package reconcile

import (
	"context"
	"fmt"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
	"arb/internal/testutil"
	"arb/internal/venue"
)

// pipelineCloser 用真实 Pipeline 实现 Closer 接口（auto_close 端到端测试用）。
type pipelineCloser struct {
	pipeline *exec.Pipeline
	store    *pgstore.Store
}

func (c *pipelineCloser) AutoClosePosition(ctx context.Context, posID int64) error {
	return c.pipeline.ClosePosition(ctx, posID)
}
func (c *pipelineCloser) ResumeClose(ctx context.Context, posID int64) error {
	return c.pipeline.ResumeClose(ctx, posID)
}
func (c *pipelineCloser) ClaimClose(ctx context.Context, posID int64) error {
	return c.store.ClaimClose(ctx, posID)
}

// reconcileTestStore 连接 ARB_RECONCILE_TEST_PG_DSN 指定的专用测试库（未设置时跳过）。
func reconcileTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_RECONCILE_TEST_PG_DSN")
	ctx := context.Background()
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// TestAutoClose_RealPipeline_SingleLeg_OpenToClosed D-189 R1 T5：
// auto_close 单腿 position（OPEN→ResumeClose）端到端测试用真实 Pipeline（非 fakeCloser）。
// 场景：position OPEN + 单腿 ticket=Y，venue 无此 ticket，历史单命中。
// auto_close → step a 回填 CLOSE leg → step c ClaimClose（OPEN→CLOSING）→ ResumeClose（CLOSING→核心→CLOSED）。
// 修复前（R1）：auto_close 直接调 ResumeClose，position 仍 OPEN → ResumeClose 拒绝 → 永久卡死。
// 修复后：先 ClaimClose 再 ResumeClose → 正常流转 CLOSED。
func TestAutoClose_RealPipeline_SingleLeg_OpenToClosed(t *testing.T) {
	s := reconcileTestStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// seed position OPEN + single leg
	var posID int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('SWAPARB:GBPUSD:52993526:mt5::1', 'SWAPARB', 'OPEN', NOW())
		RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: "52993526:mt5",
		Symbol: "GBPUSD", Side: "BUY", Qty: 0.01, OrderID: "Y", Status: "FILLED",
	}); err != nil {
		t.Fatalf("seed leg: %v", err)
	}

	// 构造真实 Pipeline：MT executor（不会被调用，broker 已无腿 → backfill 路径）
	reg := exec.NewRegistry()
	reg.Register(&fakeMTNoOrderExecutor{venue: "52993526:mt5"})
	pipeline := exec.NewPipeline(reg, s, bus.New(), exec.RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	// verifier 返回空（venue 无此 ticket → broker 已无腿）
	pipeline.SetPositionVerifier(&fakeMTVerifier{orders: map[string][]exec.OpenedOrder{
		"52993526:mt5": {},
	}})
	// calibrator 有历史 → backfillCloseFromHistory 回填 CLOSE leg
	pipeline.SetPnLCalibrator(&fakeMTCalibrator{results: map[string]*exec.HistoryPnL{
		"52993526:mt5:Y": {Profit: 5.0, ClosePrice: 1.3, Swap: 0, Commission: 0},
	}})

	// pipelineCloser 包装真实 Pipeline 实现 Closer 接口
	closer := &pipelineCloser{pipeline: pipeline, store: s}

	// 构造 Reconciler：用真实 store + pipelineCloser + fakeSettler
	r := &Reconciler{
		store:        s,
		closer:       closer,
		settler:      &fakeSettler{},
		runTimeout:   10 * time.Second,
		initialDelay: 0, // 测试无延迟
	}

	// 历史单查证器：ticket=Y 命中
	hq := &fakeHistoryQuerier{
		venueKey: "52993526:mt5",
		results:  map[string]*HistoryOrder{"Y": {Ticket: "Y", ClosePrice: 1.3, Profit: 5.0, Symbol: "GBPUSD"}},
	}
	r.SetOrderHistoryQueriers(hq)

	// venue querier：返回空（venue 无此 ticket）
	r.SetQueriers(&fakeEmptyVenueQuerier{venueKey: "52993526:mt5"})

	// Run → auto_close → ClaimClose → ResumeClose → CLOSED
	mismatches, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mismatches != 0 {
		t.Errorf("收尾成功 mismatches=%d，期望 0", mismatches)
	}

	// 验证 position CLOSED
	pos, err := s.GetPosition(ctx, posID)
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}
	if pos.Status != "CLOSED" {
		t.Errorf("期望 CLOSED（auto_close → ClaimClose → ResumeClose → closePositionCore），实际 %s", pos.Status)
	}

	// 验证有 warn 告警
	alerts, err := s.ListAlerts(ctx, 100, 0)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	foundWarn := false
	for _, a := range alerts {
		if a.Level == "warn" && contains(a.Message, "已自动对账收尾") {
			foundWarn = true
			break
		}
	}
	if !foundWarn {
		t.Error("缺少 warn 告警「已自动对账收尾」")
	}
}

// fakeMTNoOrderExecutor：MT venue executor，CloseOrder 返回 error（不应被调用，broker 已无腿）。
type fakeMTNoOrderExecutor struct {
	venue string
}

func (e *fakeMTNoOrderExecutor) Key() string { return e.venue }
func (e *fakeMTNoOrderExecutor) PlaceOrder(ctx context.Context, o exec.Order) (exec.Fill, error) {
	return exec.Fill{}, fmt.Errorf("fakeMTNoOrderExecutor: PlaceOrder should not be called (broker has no leg)")
}
func (e *fakeMTNoOrderExecutor) CloseOrder(ctx context.Context, ref exec.OrderRef) (exec.Fill, error) {
	return exec.Fill{}, fmt.Errorf("fakeMTNoOrderExecutor: CloseOrder should not be called (broker has no leg)")
}

// fakeMTVerifier：MT venue PositionVerifier。
type fakeMTVerifier struct {
	orders map[string][]exec.OpenedOrder
}

func (v *fakeMTVerifier) QueryOpenedOrders(ctx context.Context, venueKey string) ([]exec.OpenedOrder, error) {
	return v.orders[venueKey], nil
}

// fakeMTCalibrator：MT venue PnLCalibrator。
type fakeMTCalibrator struct {
	results map[string]*exec.HistoryPnL
}

func (c *fakeMTCalibrator) QueryHistoryPnL(ctx context.Context, venueKey, ticket string, fromDays int) (*exec.HistoryPnL, error) {
	return c.results[venueKey+":"+ticket], nil
}

// fakeEmptyVenueQuerier：返回空持仓的 venue querier。
type fakeEmptyVenueQuerier struct {
	venueKey string
}

func (q *fakeEmptyVenueQuerier) VenueKey() string { return q.venueKey }
func (q *fakeEmptyVenueQuerier) QueryOpenedOrders(ctx context.Context) ([]venue.Order, error) {
	return nil, nil
}
