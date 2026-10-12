// d249_s1_closed_log_test.go —— D-249 S1 第3项：CLOSED 翻译点记录测试（先红后绿）。
// 裁定（a385b84）：closePositionCore 成功 CLOSED 后写一行 CLOSED log（腿级摘要）；
// 幂等=依赖 ErrTerminalState，禁预查；重复调用不写第二行。
package exec

import (
	"context"
	"strings"
	"testing"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
)

// seedClosingPosition 构造 CLOSING position + OPEN/CLOSE legs（全 FILLED），返回 posID。
// verifier 返回空（无残留）→ closePositionCore 跳过发单 → 残量校验通过 → ClosePosition。
func seedClosingPosition(t *testing.T, s *pgstore.Store, ctx context.Context) int64 {
	t.Helper()
	reg := NewRegistry()
	exe := &fakeCountingExecutor{venue: "okx:perp", fill: Fill{OrderID: "should-not-call", Qty: 0.002, Price: 50000}}
	reg.Register(exe)
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{"okx:perp": {}}}
	p.SetPnLCalibrator(&fakePnLCalibrator{results: map[string]*HistoryPnL{
		"okx:perp:open-1": {Profit: 5.0, ClosePrice: 101.0, Swap: 0, Commission: 0},
	}})
	var posID int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('PERP:BTCUSDT:okx:perp::1', 'PERP_FUNDING', 'CLOSING', NOW())
		RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: "okx:perp",
		Symbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.002, OrderID: "open-1", Status: "FILLED",
	}); err != nil {
		t.Fatalf("seed OPEN leg: %v", err)
	}
	fullQty := 0.002
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "CLOSE", AttemptNo: 1, Venue: "okx:perp",
		Symbol: "BTC-USDT-SWAP", Side: "SELL", Qty: 0.002, OrderID: "close-full",
		Status: "FILLED", FilledQty: &fullQty,
	}); err != nil {
		t.Fatalf("seed CLOSE leg: %v", err)
	}
	return posID
}

// queryClosedLogs 查 autotrade_logs 中 status='CLOSED' 且 position_id=posID 的行，返回 reason 列表。
func queryClosedLogs(t *testing.T, s *pgstore.Store, ctx context.Context, posID int64) []string {
	t.Helper()
	rows, err := s.Pool().Query(ctx, `SELECT reason FROM autotrade_logs WHERE status='CLOSED' AND position_id=$1`, posID)
	if err != nil {
		t.Fatalf("query closed logs: %v", err)
	}
	defer rows.Close()
	var reasons []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatalf("scan: %v", err)
		}
		reasons = append(reasons, r)
	}
	return reasons
}

// TestD249_ClosePositionCore_ClosedLogWritten 验证 CLOSED 后写一行 CLOSED log（腿级摘要）。
// reason 须含 realized_pnl= 和 legs=[venue:ticket]。
func TestD249_ClosePositionCore_ClosedLogWritten(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows, autotrade_logs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	posID := seedClosingPosition(t, s, ctx)
	reg := NewRegistry()
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{"okx:perp": {}}}
	p.SetPnLCalibrator(&fakePnLCalibrator{results: map[string]*HistoryPnL{
		"okx:perp:open-1": {Profit: 5.0, ClosePrice: 101.0, Swap: 0, Commission: 0},
	}})
	if err := p.closePositionCore(ctx, posID, false); err != nil {
		t.Fatalf("closePositionCore: %v", err)
	}
	reasons := queryClosedLogs(t, s, ctx, posID)
	if len(reasons) != 1 {
		t.Fatalf("want 1 CLOSED log, got %d", len(reasons))
	}
	r := reasons[0]
	if !strings.Contains(r, "realized_pnl=") {
		t.Errorf("reason lacks realized_pnl=: %q", r)
	}
	if !strings.Contains(r, "legs=[") || !strings.Contains(r, "okx:perp:close-full") {
		t.Errorf("reason lacks legs=[venue:ticket]: %q", r)
	}
}

// TestD249_ClosePositionCore_ClosedLogIdempotent 验证重复调用只写一行 CLOSED log。
// 第二次走 ErrTerminalState 分支跳过（禁预查，依赖 store.ClosePosition 守卫）。
func TestD249_ClosePositionCore_ClosedLogIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows, autotrade_logs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	posID := seedClosingPosition(t, s, ctx)
	reg := NewRegistry()
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{"okx:perp": {}}}
	p.SetPnLCalibrator(&fakePnLCalibrator{results: map[string]*HistoryPnL{
		"okx:perp:open-1": {Profit: 5.0, ClosePrice: 101.0, Swap: 0, Commission: 0},
	}})
	// 第一次：成功 CLOSED → 写一行
	if err := p.closePositionCore(ctx, posID, false); err != nil {
		t.Fatalf("first closePositionCore: %v", err)
	}
	// 第二次：已 CLOSED → ErrTerminalState 分支返回 nil → 不写 CLOSED log
	if err := p.closePositionCore(ctx, posID, false); err != nil {
		t.Fatalf("second closePositionCore (idempotent): %v", err)
	}
	reasons := queryClosedLogs(t, s, ctx, posID)
	if len(reasons) != 1 {
		t.Errorf("want 1 CLOSED log after repeat, got %d (幂等失败)", len(reasons))
	}
}
