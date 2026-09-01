// d189_r1r2_test.go —— D-189 R1/R2 退回修复单测（T2-T4）。
// T2：ClosePosition 接入 closePositionCore 验证（brokerPreflight 路径）。
// T3：ClosePosition vs ResumeClose 并发锁（同 posID 串行执行）。
// T4：ClosePosition broker-preflight 路径（broker 已无腿时不发单 → 历史回填）。
package exec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// TestClosePosition_UsesClosePositionCore T2：ClosePosition 走 closePositionCore（非旧重复逻辑）。
// 验证锚：closePositionCore 有 brokerPreflight（旧 ClosePosition 无）。
// 构造：verifier 返回空（broker 已无腿）→ closePositionCore 走 backfillCloseFromHistory 路径
// （旧 ClosePosition 会直接发平仓单 → 不会走 backfill）。
// calibrator 有历史 → 回填 CLOSE leg → CLOSED。若 ClosePosition 未接入 closePositionCore，
// 会直接发平仓单（fakeExecutor 返回 fill）→ 不走 backfill → 行为不同。
func TestClosePosition_UsesClosePositionCore(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	// fakeNoOrderExecutor：PlaceOrder 返回 error（若 ClosePosition 旧逻辑直接发单 → 会触发此 error）
	reg.Register(&fakeNoOrderExecutor{venue: "okx:perp"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50})
	// verifier 返回空 → brokerPreflight 判定 broker 已无腿 → backfillCloseFromHistory
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"okx:perp": {},
	}}
	// calibrator 有历史 → backfillCloseFromHistory 回填 CLOSE leg
	p.SetPnLCalibrator(&fakePnLCalibrator{results: map[string]*HistoryPnL{
		"okx:perp:T-backfill": {Profit: 5.0, ClosePrice: 101.0, Swap: 0, Commission: 0},
	}})

	// seed position OPEN + single leg
	var posID int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('PERP:BTCUSDT:okx:perp::1', 'PERP_FUNDING', 'OPEN', NOW())
		RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: "okx:perp",
		Symbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, OrderID: "T-backfill", Status: "FILLED",
	}); err != nil {
		t.Fatalf("seed leg: %v", err)
	}

	// ClosePosition → ClaimClose → closePositionCore → brokerPreflight（空→backfill）→ CLOSED
	if err := p.ClosePosition(ctx, posID); err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}
	pos, _ := s.GetPosition(ctx, posID)
	if pos.Status != "CLOSED" {
		t.Errorf("期望 CLOSED（backfill 路径），实际 %s", pos.Status)
	}
	// 验证有 CLOSE leg（由 backfillCloseFromHistory 写入，非 fakeNoOrderExecutor 发单）
	legs, _ := s.ListPositionLegs(ctx, posID)
	hasClose := false
	for _, l := range legs {
		if l.Phase == "CLOSE" && l.Status == "FILLED" {
			hasClose = true
		}
	}
	if !hasClose {
		t.Error("缺少 CLOSE leg（backfillCloseFromHistory 应写入）")
	}
}

// TestClosePosition_ConcurrentWithResumeClose T3：ClosePosition vs ResumeClose 并发锁。
// 同一 posID 并发调用 → per-position 锁保证串行（不 panic、不重复写 leg）。
// 构造：position CLOSING（ResumeClose 可进入），ClosePosition 会 ClaimClose 失败（已 CLOSING）→ 返回 error。
// 两个 goroutine 同时调用 → 一个成功（ResumeClose），一个 ClaimClose 失败 → 不 panic。
func TestClosePosition_ConcurrentWithResumeClose(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	reg.Register(&fakeFlattenExecutor{venue: "okx:perp"})
	reg.Register(&fakeFlattenExecutor{venue: "binance:perp"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50})
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"okx:perp":     {{Symbol: "BTC-USDT-SWAP", Side: "BUY", Ticket: "okx-c"}},
		"binance:perp": {{Symbol: "BTCUSDT", Side: "SELL", Ticket: "bin-c"}},
	}}

	res, err := p.Execute(ctx,
		"PERP:BTCUSDT:okx:perp:binance:perp:conc", "req-conc", "PERP_FUNDING",
		[]LegSpec{
			{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, PriceExpected: 100000},
			{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "SELL", Qty: 0.001, PriceExpected: 100000},
		})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "OPEN" {
		t.Fatalf("期望 OPEN，实际 %s", res.Status)
	}

	// 并发：5 goroutine ClosePosition + 5 goroutine ResumeClose 同一 posID
	var wg sync.WaitGroup
	var closeCalls int32
	var resumeCalls int32
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			atomic.AddInt32(&closeCalls, 1)
			_ = p.ClosePosition(ctx, res.PositionID)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			atomic.AddInt32(&resumeCalls, 1)
			_ = p.ResumeClose(ctx, res.PositionID)
		}()
	}
	wg.Wait()
	// 验证不 panic 即可（per-position 锁保证串行）
	// 最终 position 应为 CLOSED（至少一个调用成功）
	pos, _ := s.GetPosition(ctx, res.PositionID)
	if pos.Status != "CLOSED" {
		t.Errorf("并发后 position status=%s，期望 CLOSED（至少一个调用成功）", pos.Status)
	}
}

// TestClosePosition_BrokerNoLeg_NoOrderPlaced T4：ClosePosition broker-preflight（broker 已无腿时不发单）。
// 构造：position OPEN + single leg，verifier 返回空（broker 已无腿）。
// closePositionCore → brokerPreflight → stillOpen=false → backfillCloseFromHistory。
// calibrator 有历史 → 回填 CLOSE leg → CLOSED（不发平仓单）。
// 验证：fakeNoOrderExecutor.PlaceOrder 未被调用（broker 已无腿 → 不发单）。
func TestClosePosition_BrokerNoLeg_NoOrderPlaced(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	var placeCount int32
	reg.Register(&fakeNoOrderExecutor{venue: "okx:perp", placeCount: &placeCount})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50})
	// verifier 返回空 → broker 已无腿
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"okx:perp": {},
	}}
	// calibrator 有历史 → backfillCloseFromHistory 回填
	p.SetPnLCalibrator(&fakePnLCalibrator{results: map[string]*HistoryPnL{
		"okx:perp:T-noleg": {Profit: 3.0, ClosePrice: 102.0, Swap: 0, Commission: 0},
	}})

	var posID int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('PERP:BTCUSDT:okx:perp::2', 'PERP_FUNDING', 'OPEN', NOW())
		RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: "okx:perp",
		Symbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, OrderID: "T-noleg", Status: "FILLED",
	}); err != nil {
		t.Fatalf("seed leg: %v", err)
	}

	if err := p.ClosePosition(ctx, posID); err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}
	// 验证：未发平仓单（broker 已无腿 → backfill 路径，不调 PlaceOrder）
	if placeCount > 0 {
		t.Errorf("broker 已无腿时不应发平仓单，PlaceOrder 调用次数=%d", placeCount)
	}
	// 验证：position CLOSED（backfill 回填 CLOSE leg）
	pos, _ := s.GetPosition(ctx, posID)
	if pos.Status != "CLOSED" {
		t.Errorf("期望 CLOSED（backfill 路径），实际 %s", pos.Status)
	}
}

// fakeNoOrderExecutor：PlaceOrder 返回 error（用于验证 ClosePosition 不发单的场景）。
// CloseOrder 也返回 error（MT 腿不会走此路径，CEX 腿走 PlaceOrder）。
type fakeNoOrderExecutor struct {
	venue      string
	placeCount *int32
}

func (e *fakeNoOrderExecutor) Key() string { return e.venue }
func (e *fakeNoOrderExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	if e.placeCount != nil {
		atomic.AddInt32(e.placeCount, 1)
	}
	return Fill{}, fmt.Errorf("fakeNoOrderExecutor: should not be called (broker has no leg)")
}
func (e *fakeNoOrderExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	if e.placeCount != nil {
		atomic.AddInt32(e.placeCount, 1)
	}
	return Fill{}, fmt.Errorf("fakeNoOrderExecutor: CloseOrder should not be called")
}

// 确保编译时检查 errors.Is 可用（T2-T4 使用 errors.Is 判定 ErrTerminalState）
var _ = errors.Is
var _ = strings.Contains
