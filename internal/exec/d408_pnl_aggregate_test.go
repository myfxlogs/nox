// d408_pnl_aggregate_test.go —— D-408 S2：realized_pnl 聚合缺口（残腿重试丢早平腿盈亏）。
// 红锚=498 复刻：前次尝试已平腿（CLOSE FILLED 已持久化）+ 本次回填腿 →
// realized_pnl=双腿价格差汇总，修正前循环内累加两条 continue 全跳过=0/只记末腿。
package exec

import (
	"context"
	"math"
	"testing"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// d408Truncate 清仓相关表（本 fixture 独占）。
func d408Truncate(t *testing.T, s *pgstore.Store) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(),
		`TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// d408Seed498 498 复刻：pos CLOSING + leg1(okx BUY @3900) 已有 CLOSE FILLED(@4000, 前次尝试平掉)
// + leg2(mt5 SELL @4100) 无 CLOSE（本次 resume 经历史回填 @4180）。
// 期望：leg1 PnL=(4000−3900)×1=+100；leg2 PnL=(4180−4100)×1×(−1)=−80；合计 +20。
// 修正前：leg1 continue（已有 CLOSE）、leg2 backfill continue → totalPnL=0。
func d408Seed498(t *testing.T, s *pgstore.Store) int64 {
	t.Helper()
	ctx := context.Background()
	var posID int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('XASSET_CARRY:XPTUSD:3001157:mt5:okx:perp', 'XASSET_CARRY', 'CLOSING', NOW())
		RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	q1, p3900, p4000, p4100 := 1.0, 3900.0, 4000.0, 4100.0
	for _, l := range []pgstore.PositionLeg{
		{PositionID: posID, LegNo: 1, Phase: "OPEN", Venue: "okx:perp", Symbol: "XPT-USDT-SWAP",
			Side: "BUY", Qty: 1, Status: "FILLED", OrderID: "okx-t1",
			PriceActual: &p3900, FilledQty: &q1, AvgFillPrice: &p3900},
		// leg1 前次尝试已平：CLOSE FILLED 已持久化（重试时走 continue 分支）
		{PositionID: posID, LegNo: 1, Phase: "CLOSE", Venue: "okx:perp", Symbol: "XPT-USDT-SWAP",
			Side: "SELL", Qty: 1, Status: "FILLED", OrderID: "okx-c1",
			PriceActual: &p4000, FilledQty: &q1, AvgFillPrice: &p4000},
		{PositionID: posID, LegNo: 2, Phase: "OPEN", Venue: "3001157:mt5", Symbol: "XPTUSD",
			Side: "SELL", Qty: 1, Status: "FILLED", OrderID: "mt-t2",
			PriceActual: &p4100, FilledQty: &q1, AvgFillPrice: &p4100},
	} {
		if err := s.CreatePositionLeg(ctx, &l); err != nil {
			t.Fatalf("seed leg: %v", err)
		}
	}
	return posID
}

// d408Pipeline 498 复刻管线：verifier 空在仓（leg2 → 历史回填）、calibrator 只对
// mt-t2 命中（ClosePrice=4180 回填用；calibrate 部分命中不覆盖=498 同款 note 路径）。
func d408Pipeline(s *pgstore.Store) *Pipeline {
	reg := NewRegistry()
	reg.Register(&fakeNoOrderExecutor{venue: "okx:perp"})
	reg.Register(&fakeNoOrderExecutor{venue: "3001157:mt5"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{}, "demo")
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"okx:perp":    {},
		"3001157:mt5": {},
	}}
	p.SetPnLCalibrator(&fakePnLCalibrator{results: map[string]*HistoryPnL{
		"3001157:mt5:mt-t2": {Profit: -80.0, ClosePrice: 4180.0}, // backfill 取 ClosePrice；calibrate 部分命中不覆盖
	}})
	return p
}

// TestD408_ResumeClose_EarlyLegPnL 红锚：498 形态——前次已平腿+本次回填腿全计入 realized_pnl。
func TestD408_ResumeClose_EarlyLegPnL(t *testing.T) {
	s := testStore(t)
	d408Truncate(t, s)
	posID := d408Seed498(t, s)
	p := d408Pipeline(s)
	if err := p.ResumeClose(context.Background(), posID); err != nil {
		t.Fatalf("ResumeClose: %v", err)
	}
	pos, err := s.GetPosition(context.Background(), posID)
	if err != nil || pos == nil {
		t.Fatalf("get pos: %v", err)
	}
	if pos.Status != "CLOSED" {
		t.Fatalf("status=%s want CLOSED", pos.Status)
	}
	if pos.RealizedPnl == nil || math.Abs(*pos.RealizedPnl-20.0) > 0.01 {
		t.Fatalf("realized_pnl=%v want ≈+20（+100 okx 腿 −80 mt 腿）", pos.RealizedPnl)
	}
}

// TestD408_ResumeClose_CalibrateOverride 既有语义保留：calibrate 全腿命中仍覆盖。
func TestD408_ResumeClose_CalibrateOverride(t *testing.T) {
	s := testStore(t)
	d408Truncate(t, s)
	posID := d408Seed498(t, s)
	p := d408Pipeline(s)
	// 双腿 ticket 均命中 → calibrate 全命中覆盖（auth=+1000−800=+200，与同步 +20 无关）。
	p.SetPnLCalibrator(&fakePnLCalibrator{results: map[string]*HistoryPnL{
		"okx:perp:okx-t1":   {Profit: 1000.0},
		"3001157:mt5:mt-t2": {Profit: -800.0, ClosePrice: 4180.0},
	}})
	if err := p.ResumeClose(context.Background(), posID); err != nil {
		t.Fatalf("ResumeClose: %v", err)
	}
	pos, _ := s.GetPosition(context.Background(), posID)
	if pos.RealizedPnl == nil || math.Abs(*pos.RealizedPnl-200.0) > 0.01 {
		t.Fatalf("calibrate 全命中应覆盖为 auth=+200, got %v", pos.RealizedPnl)
	}
}

// TestD408_SumClosedLegsPnL 纯函数矩阵：正常双腿/部分成交按 FilledQty/缺价计 0+alert/无 CLOSE 腿不计。
func TestD408_SumClosedLegsPnL(t *testing.T) {
	s := testStore(t)
	d408Truncate(t, s)
	ctx := context.Background()
	p3900, p4100, p4000, p4180 := 3900.0, 4100.0, 4000.0, 4180.0
	q1, half := 1.0, 0.5
	openLegs := []pgstore.PositionLeg{
		{PositionID: 1, LegNo: 1, Phase: "OPEN", Venue: "okx:perp", Symbol: "XPT-USDT-SWAP",
			Side: "BUY", Qty: 1, Status: "FILLED", PriceActual: &p3900, FilledQty: &q1},
		{PositionID: 1, LegNo: 2, Phase: "OPEN", Venue: "3001157:mt5", Symbol: "XPTUSD",
			Side: "SELL", Qty: 1, Status: "FILLED", PriceActual: &p4100, FilledQty: &q1},
	}
	p := &Pipeline{store: s}
	mkClose := func(legNo int16, price, qty float64) pgstore.PositionLeg {
		return pgstore.PositionLeg{PositionID: 1, LegNo: legNo, Phase: "CLOSE", Status: "FILLED",
			PriceActual: &price, AvgFillPrice: &price, FilledQty: &qty, Qty: qty}
	}
	// ①双腿全平：+100−80=+20
	got := p.sumClosedLegsPnL(ctx, 1, openLegs, []pgstore.PositionLeg{
		mkClose(1, p4000, q1), mkClose(2, p4180, q1)}, 1.0)
	if math.Abs(got-20.0) > 0.01 {
		t.Errorf("①双腿 want +20 got %.2f", got)
	}
	// ②部分成交按 FilledQty：leg1 close filled=0.5 → +50；leg2 −80 → −30
	got = p.sumClosedLegsPnL(ctx, 1, openLegs, []pgstore.PositionLeg{
		mkClose(1, p4000, half), mkClose(2, p4180, q1)}, 1.0)
	if math.Abs(got-(-30.0)) > 0.01 {
		t.Errorf("②部分成交 want −30 got %.2f", got)
	}
	// ③CLOSE 腿缺 PriceActual → 该腿计 0 + critical alert，不 panic。
	// （alerts.rule_id FK→rules：与既有调用方同约定传 posID，先种一条 rule 使告警可落）
	var ruleID int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO rules (name, kind, cond, level) VALUES ('d408', 'sys', 'x', 'critical')
		 ON CONFLICT (name) DO UPDATE SET name='d408' RETURNING id`).Scan(&ruleID); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	noPrice := pgstore.PositionLeg{PositionID: 1, LegNo: 1, Phase: "CLOSE", Status: "FILLED",
		FilledQty: &q1, Qty: q1}
	got = p.sumClosedLegsPnL(ctx, ruleID, openLegs, []pgstore.PositionLeg{noPrice, mkClose(2, p4180, q1)}, 1.0)
	if math.Abs(got-(-80.0)) > 0.01 {
		t.Errorf("③缺价腿计 0 want −80 got %.2f", got)
	}
	var alertN int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='critical'`).Scan(&alertN); err != nil || alertN == 0 {
		t.Errorf("③缺价应落 critical alert, n=%d err=%v", alertN, err)
	}
	// ④OPEN 腿无 CLOSE → 不累计（残腿校验另行拦阻，不虚增）
	got = p.sumClosedLegsPnL(ctx, 1, openLegs, []pgstore.PositionLeg{mkClose(2, p4180, q1)}, 1.0)
	if math.Abs(got-(-80.0)) > 0.01 {
		t.Errorf("④无 CLOSE 腿不计 want −80 got %.2f", got)
	}
}
