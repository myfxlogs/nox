// 单测：SWAPARB 入场门禁三条——厚度+回收期往返+集中度。
// 纯函数测试（carryThicknessGate/paybackRoundTripGate）+ DB 集成测试（concentrationGate 471/472 场景）。
package autotrade

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// TestD180_CarryThicknessGate_SettledPass settled 源不折扣，净息差 ≥ 门槛 → 放行。
func TestD180_CarryThicknessGate_SettledPass(t *testing.T) {
	g := carryThicknessGate(2.0, swapSrcSettled, 1.0, 0.5)
	if g.reject {
		t.Errorf("settled 2.0 >= 1.0: want pass, got reject: %s", g.reason)
	}
}

// TestD180_CarryThicknessGate_SpecDiscountReject spec 源 ×0.5 折扣后 < 门槛 → 拒绝。
func TestD180_CarryThicknessGate_SpecDiscountReject(t *testing.T) {
	// spec 1.5 × 0.5 = 0.75 < 1.0 → 拒绝
	g := carryThicknessGate(1.5, swapSrcSpec, 1.0, 0.5)
	if !g.reject {
		t.Errorf("spec 1.5×0.5=0.75 < 1.0: want reject, got pass")
	}
}

// TestD180_CarryThicknessGate_SpecDiscountPass spec 源 ×0.5 折扣后 ≥ 门槛 → 放行。
func TestD180_CarryThicknessGate_SpecDiscountPass(t *testing.T) {
	// spec 3.0 × 0.5 = 1.5 >= 1.0 → 放行
	g := carryThicknessGate(3.0, swapSrcSpec, 1.0, 0.5)
	if g.reject {
		t.Errorf("spec 3.0×0.5=1.5 >= 1.0: want pass, got reject: %s", g.reason)
	}
}

// TestD180_CarryThicknessGate_Disabled MinNetCarryBps<=0 → 不校验。
func TestD180_CarryThicknessGate_Disabled(t *testing.T) {
	g := carryThicknessGate(0.0, swapSrcNone, 0, 0.5)
	if g.reject {
		t.Errorf("MinNetCarryBps=0: want pass (disabled), got reject")
	}
}

// TestD180_PaybackRoundTripGate_Pass 回收期 ≤ MaxPaybackDays → 放行。
func TestD180_PaybackRoundTripGate_Pass(t *testing.T) {
	// 2×3 / 1.0 = 6 天 ≤ 30 → 放行
	g := paybackRoundTripGate(3.0, 1.0, 30)
	if g.reject {
		t.Errorf("2×3/1=6 days <= 30: want pass, got reject: %s", g.reason)
	}
}

// TestD180_PaybackRoundTripGate_Reject 回收期 > MaxPaybackDays → 拒绝。
func TestD180_PaybackRoundTripGate_Reject(t *testing.T) {
	// 2×20 / 1.0 = 40 天 > 30 → 拒绝
	g := paybackRoundTripGate(20.0, 1.0, 30)
	if !g.reject {
		t.Errorf("2×20/1=40 days > 30: want reject, got pass")
	}
}

// TestD180_PaybackRoundTripGate_NetSwapZeroReject 净息差 ≤0 → 拒绝（无回收期语义）。
func TestD180_PaybackRoundTripGate_NetSwapZeroReject(t *testing.T) {
	g := paybackRoundTripGate(3.0, 0.0, 30)
	if !g.reject {
		t.Errorf("netSwap=0: want reject (no carry), got pass")
	}
}

// TestD180_PaybackRoundTripGate_Disabled MaxPaybackDays<=0 → 不校验。
func TestD180_PaybackRoundTripGate_Disabled(t *testing.T) {
	g := paybackRoundTripGate(1000.0, 0.001, 0)
	if g.reject {
		t.Errorf("MaxPaybackDays=0: want pass (disabled), got reject")
	}
}

// TestD180_PaybackRoundTrip_2xVs1x 2× vs 1× 拒绝率差异验证。
// spreadBps=15, netSwapBps=1.0：1×=15 天（≤30 放行），2×=30 天（≤30 放行，边界）。
// spreadBps=16, netSwapBps=1.0：1×=16 天（≤30 放行），2×=32 天（>30 拒绝）。
func TestD180_PaybackRoundTrip_2xVs1x(t *testing.T) {
	// 1× 模拟（spreadBps / netSwapBps，非 2×）
	oneX := 16.0 / 1.0
	if oneX > 30 {
		t.Errorf("1× 16 days: want <= 30 (pass), got %.1f", oneX)
	}
	// 2× 实际门禁
	g := paybackRoundTripGate(16.0, 1.0, 30)
	if !g.reject {
		t.Errorf("2× 32 days > 30: want reject (2× symmetric), got pass")
	}
}

// TestD180_ConcentrationGate_Empty 无活跃仓 → 放行（新仓两腿各 1 ≤ 2）。
func TestD180_ConcentrationGate_Empty(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	e := &Engine{store: s}
	g := e.concentrationGate(ctx, "AUDJPY", "vA:mt5", "vB:mt5", 2)
	if g.reject {
		t.Errorf("empty: want pass, got reject: %s", g.reason)
	}
}

// TestD180_ConcentrationGate_ThirdLegReject 471/472 场景：同 venue 已有 2 腿，新仓第三腿 → 拒绝。
func TestD180_ConcentrationGate_ThirdLegReject(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 模拟 471/472：两条 SWAPARB 持仓，vA 各有一腿（共 2 腿 vA×AUDJPY）
	for i := 0; i < 2; i++ {
		pos := &pgstore.Position{
			OpportunityID: "SWAPARB:AUDJPY:vA:vB:170000000000" + string(rune('0'+i)),
			Type:          "SWAPARB",
			Status:        "OPEN",
		}
		id, err := s.CreatePosition(ctx, pos)
		if err != nil {
			t.Fatalf("create pos %d: %v", i, err)
		}
		price := 100.0
		if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID: id, LegNo: 1, Phase: "OPEN", Venue: "vA:mt5", Symbol: "AUDJPY",
			Side: "SELL", Qty: 1.0, Status: "FILLED", PriceActual: &price,
		}); err != nil {
			t.Fatalf("create leg %d: %v", i, err)
		}
	}
	e := &Engine{store: s}
	// 新仓 vA:mt5 × AUDJPY 已有 2 腿 +1 = 3 > 2 → 拒绝
	g := e.concentrationGate(ctx, "AUDJPY", "vA:mt5", "vB:mt5", 2)
	if !g.reject {
		t.Errorf("471/472 third leg: want reject, got pass")
	}
}

// TestD180_ConcentrationGate_SecondLegPass 同 venue 已有 1 腿，新仓第二腿 → 放行（1+1=2 ≤ 2）。
func TestD180_ConcentrationGate_SecondLegPass(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 一条 SWAPARB 持仓，vA 有一腿
	pos := &pgstore.Position{
		OpportunityID: "SWAPARB:AUDJPY:vA:vB:1700000000001",
		Type:          "SWAPARB",
		Status:        "OPEN",
	}
	id, err := s.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("create pos: %v", err)
	}
	price := 100.0
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 1, Phase: "OPEN", Venue: "vA:mt5", Symbol: "AUDJPY",
		Side: "SELL", Qty: 1.0, Status: "FILLED", PriceActual: &price,
	}); err != nil {
		t.Fatalf("create leg: %v", err)
	}
	e := &Engine{store: s}
	// 新仓 vA:mt5 × AUDJPY 已有 1 腿 +1 = 2 ≤ 2 → 放行
	g := e.concentrationGate(ctx, "AUDJPY", "vA:mt5", "vB:mt5", 2)
	if g.reject {
		t.Errorf("second leg (1+1=2 <= 2): want pass, got reject: %s", g.reason)
	}
}

// TestD180_ConcentrationGate_DifferentSymbolPass 同 venue 不同 symbol → 放行（键不同）。
func TestD180_ConcentrationGate_DifferentSymbolPass(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// vA 已有 2 腿 AUDJPY，新仓 GBPJPY → 键不同，放行
	for i := 0; i < 2; i++ {
		pos := &pgstore.Position{
			OpportunityID: "SWAPARB:AUDJPY:vA:vB:170000000000" + string(rune('0'+i)),
			Type:          "SWAPARB",
			Status:        "OPEN",
		}
		id, err := s.CreatePosition(ctx, pos)
		if err != nil {
			t.Fatalf("create pos %d: %v", i, err)
		}
		price := 100.0
		if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID: id, LegNo: 1, Phase: "OPEN", Venue: "vA:mt5", Symbol: "AUDJPY",
			Side: "SELL", Qty: 1.0, Status: "FILLED", PriceActual: &price,
		}); err != nil {
			t.Fatalf("create leg %d: %v", i, err)
		}
	}
	e := &Engine{store: s}
	// 新仓 vA:mt5 × GBPJPY → 键 vA\x00GBPJPY 计数 0 +1 = 1 ≤ 2 → 放行
	g := e.concentrationGate(ctx, "GBPJPY", "vA:mt5", "vB:mt5", 2)
	if g.reject {
		t.Errorf("different symbol: want pass, got reject: %s", g.reason)
	}
}

// TestD180_ConcentrationGate_Disabled MaxLegsPerSymbol<=0 → 不校验。
func TestD180_ConcentrationGate_Disabled(t *testing.T) {
	s := testAccumStore(t)
	e := &Engine{store: s}
	g := e.concentrationGate(context.Background(), "AUDJPY", "vA:mt5", "vB:mt5", 0)
	if g.reject {
		t.Errorf("MaxLegsPerSymbol=0: want pass (disabled), got reject")
	}
}

// TestD180_LegVenueBare bare venue 提取。
func TestD180_LegVenueBare(t *testing.T) {
	if got := legVenueBare("61585565:mt5"); got != "61585565" {
		t.Errorf("mt5: want 61585565, got %s", got)
	}
	if got := legVenueBare("vA"); got != "vA" {
		t.Errorf("no colon: want vA, got %s", got)
	}
}

// TestD180_S4Integration_471_472Regression 471/472 场景回归：已有 2 腿 + 厚度/回收期门禁联动。
// 验证三门禁串行：厚度放行 → 回收期放行 → 集中度拒绝（471/472 第三腿）。
func TestD180_S4Integration_471_472Regression(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	// seed swap facts：net swap = 5 + (-3) = 2 > 0（厚度放行）
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	// 模拟 471/472：vA 已有 2 腿 AUDJPY
	for i := 0; i < 2; i++ {
		pos := &pgstore.Position{
			OpportunityID: "SWAPARB:AUDJPY:vA:vB:170000000000" + string(rune('0'+i)),
			Type:          "SWAPARB",
			Status:        "OPEN",
		}
		id, err := s.CreatePosition(ctx, pos)
		if err != nil {
			t.Fatalf("create pos %d: %v", i, err)
		}
		price := 100.0
		if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID: id, LegNo: 1, Phase: "OPEN", Venue: "vA:mt5", Symbol: "AUDJPY",
			Side: "SELL", Qty: 1.0, Status: "FILLED", PriceActual: &price,
		}); err != nil {
			t.Fatalf("create leg %d: %v", i, err)
		}
	}
	e := &Engine{store: s, cfg: gate.Config{
		MinNetCarryBps:   1.0,
		SpecDiscount:     0.5,
		MaxPaybackDays:   30,
		MaxLegsPerSymbol: 2,
	}}
	// 厚度门禁：spec 源 netSwap=2.0 × 0.5 = 1.0 >= 1.0 → 放行
	g1 := carryThicknessGate(2.0, swapSrcSpec, e.cfg.MinNetCarryBps, e.cfg.SpecDiscount)
	if g1.reject {
		t.Fatalf("thickness: want pass, got reject: %s", g1.reason)
	}
	// 回收期门禁：2×3 / 2.0 = 3 天 ≤ 30 → 放行
	g2 := paybackRoundTripGate(3.0, 2.0, e.cfg.MaxPaybackDays)
	if g2.reject {
		t.Fatalf("payback: want pass, got reject: %s", g2.reason)
	}
	// 集中度门禁：vA×AUDJPY 已有 2 腿 +1 = 3 > 2 → 拒绝（471/472 第三腿）
	g3 := e.concentrationGate(ctx, "AUDJPY", "vA:mt5", "vB:mt5", e.cfg.MaxLegsPerSymbol)
	if !g3.reject {
		t.Fatal("concentration 471/472 third leg: want reject, got pass")
	}
}
