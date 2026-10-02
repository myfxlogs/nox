// d422_calibrate_test.go —— D-422 S1：混合校准 + CEX 费用台账测试（先红后绿）。
package exec

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// TestD422_CalibrateMixed 混合仓（MT FILLED + CEX FILLED）：
// authPnL = MT 权威 Profit×fx + CEX 公式价 PnL（legPricePnL 同 sumClosedLegsPnL 口径）。
func TestD422_CalibrateMixed(t *testing.T) {
	openPx := 100.0
	legs := []pgstore.PositionLeg{
		{LegNo: 0, Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", OrderID: "T1",
			Symbol: "XAUUSD", Side: "BUY", Qty: 1, PriceActual: &openPx},
		{LegNo: 1, Phase: "OPEN", Status: "FILLED", Venue: "okx:perp", OrderID: "ord1",
			Symbol: "XAU-USDT-SWAP", Side: "SELL", Qty: 2, PriceActual: &openPx},
	}
	closePx := 110.0
	closeLegs := []pgstore.PositionLeg{
		{LegNo: 0, Phase: "CLOSE", Status: "FILLED", Venue: "vA:mt5", PriceActual: &closePx},
		{LegNo: 1, Phase: "CLOSE", Status: "FILLED", Venue: "okx:perp", PriceActual: &closePx},
	}
	p := &Pipeline{pnlCalibrator: &fakePnLCalibrator{results: map[string]*HistoryPnL{
		"vA:mt5:T1": {Profit: 50.0},
	}}}
	auth, ok, est := p.calibratePnL(context.Background(), legs, closeLegs, 1.0)
	if !ok {
		t.Fatalf("mixed calibrate want ok=true (CEX leg uses formula price), got ok=false est=%v", est)
	}
	// MT 权威 50×1 + CEX 公式 (110−100)×(−1)×2×1×1 = −20 → 30
	want := 50.0 - 20.0
	if math.Abs(auth-want) > 1e-9 {
		t.Fatalf("authPnL = %v, want %v（MT Profit 50 + CEX 公式 −20）", auth, want)
	}
}

// TestD422_CalibratePureCEX 纯 CEX 仓：无可校准腿 → ok=false 但 est=false（不写 pnl_estimated，
// 公式价对 CEX 即权威非估算）。
func TestD422_CalibratePureCEX(t *testing.T) {
	px := 100.0
	legs := []pgstore.PositionLeg{
		{LegNo: 0, Phase: "OPEN", Status: "FILLED", Venue: "okx:perp", OrderID: "o1",
			Symbol: "X", Side: "BUY", Qty: 1, PriceActual: &px},
	}
	p := &Pipeline{pnlCalibrator: &fakePnLCalibrator{results: map[string]*HistoryPnL{}}}
	_, ok, est := p.calibratePnL(context.Background(), legs, nil, 1.0)
	if ok {
		t.Fatal("pure CEX want ok=false（无 MT 权威覆盖）")
	}
	if est {
		t.Fatal("pure CEX want est=false（无可校准腿≠估算，禁写 pnl_estimated）")
	}
}

// TestD422_CalibrateMTMiss MT 腿校准未命中 → est=true（写 pnl_estimated 由调用方执行）。
func TestD422_CalibrateMTMiss(t *testing.T) {
	px := 100.0
	legs := []pgstore.PositionLeg{
		{LegNo: 0, Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", OrderID: "T9",
			Symbol: "XAUUSD", Side: "BUY", Qty: 1, PriceActual: &px},
	}
	p := &Pipeline{pnlCalibrator: &fakePnLCalibrator{results: map[string]*HistoryPnL{}}}
	_, ok, est := p.calibratePnL(context.Background(), legs, nil, 1.0)
	if ok {
		t.Fatal("MT miss want ok=false")
	}
	if !est {
		t.Fatal("MT miss want est=true（可校准腿未命中=真估算）")
	}
}

// TestD422_CloseLegFeeUSD 平仓路径 CLOSE 腿 fee_usd 落库（Fill.FeeUSD 正数成本，0→NULL）。
func TestD422_CloseLegFeeUSD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	reg.Register(&fakeFlattenExecutor{venue: "okx:perp", feeUSD: 0.55})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.SetContractSizeResolver(permissiveCSResolver{})
	p.verifier = &fakeVerifier{drainAfter: 1, orders: map[string][]OpenedOrder{
		"okx:perp": {{Symbol: "BTC-USDT-SWAP", Side: "BUY", Ticket: "broker-side-view"}},
	}}
	var posID int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('opp-d422','XASSET_CARRY','OPEN',0) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed pos: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: "okx:perp",
		Symbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, OrderID: "T-d422", Status: "FILLED",
	}); err != nil {
		t.Fatalf("seed leg: %v", err)
	}
	if err := p.ClosePosition(ctx, posID); err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}
	var fee *float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT fee_usd FROM position_legs WHERE position_id=$1 AND phase='CLOSE'`, posID).Scan(&fee); err != nil {
		t.Fatalf("query close leg: %v", err)
	}
	if fee == nil || *fee != 0.55 {
		t.Fatalf("CLOSE leg fee_usd = %v, want 0.55（Fill.FeeUSD 落库）", fee)
	}
}

// TestD422_CloseCashflowFeeMerge writeCloseCashflows：commission*fx + feeUSD 合并单条
// kind=commission（防双口径/幂等键互吞）。
func TestD422_CloseCashflowFeeMerge(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var posID int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('opp-d422cf','XASSET_CARRY','CLOSED',0) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed pos: %v", err)
	}
	p := &Pipeline{store: s}
	// D-013：commission=1.0（计价币）×fx=2 + fee=0.5（已 USD）→ 腿键 commission −2.5
	// （生产路径=腿 fee_usd 落库时 Ensure 自动写行；显式 amt 仅供无 leg: 行的历史补写）。
	fee25 := 2.5
	leg := pgstore.PositionLeg{PositionID: posID, LegNo: 0, Phase: "CLOSE", AttemptNo: 1,
		Venue: "okx:perp", Symbol: "X", Side: "BUY", Qty: 1, Status: "FILLED", FeeUSD: &fee25}
	if err := s.CreatePositionLeg(ctx, &leg); err != nil {
		t.Fatalf("create close leg: %v", err)
	}
	if err := p.writeCloseCashflows(ctx, posID, leg, 0, 1.0, 0.5, 2.0); err != nil {
		t.Fatalf("writeCloseCashflows: %v", err)
	}
	rows := 0
	var amt float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT amount_usd, count(*) OVER () FROM position_cashflows WHERE position_id=$1 AND kind='commission'`, posID).Scan(&amt, &rows); err != nil {
		t.Fatalf("query cashflow: %v", err)
	}
	if rows != 1 {
		t.Fatalf("commission rows = %d, want 1（合并口径单条）", rows)
	}
	if amt != -2.5 {
		t.Fatalf("commission amount = %v, want −2.5（1.0×2 + 0.5）", amt)
	}
}

// TestD422_OpenLegFeeCashflow OPEN 腿 fee>0 → 同点写 kind=commission −feeUSD 现金流（入场费入账）。
func TestD422_OpenLegFeeCashflow(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_EXEC_TEST_PG_DSN")
	s := testStore(t)
	conn := seedConn(t, dsn)
	ctx := context.Background()
	var posID int64
	if err := conn.QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('opp-d422open','XASSET_CARRY','OPEN',0) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed pos: %v", err)
	}
	p := &Pipeline{store: s}
	legs := []LegSpec{{VenueKey: "okx:perp", VenueSymbol: "X", Side: "BUY", Qty: 1, PriceExpected: 100}}
	results := []LegResult{{VenueKey: "okx:perp", OrderID: "o1", Price: 100, Qty: 1, Status: "FILLED", FeeUSD: 0.77}}
	if err := p.persistOpenLegs(ctx, "req-d422", posID, legs, results); err != nil {
		t.Fatalf("persistOpenLegs: %v", err)
	}
	var amt *float64
	if err := conn.QueryRow(ctx,
		`SELECT amount_usd FROM position_cashflows WHERE position_id=$1 AND kind='commission' AND leg_no=0`, posID).Scan(&amt); err != nil {
		t.Fatalf("query open fee cashflow: %v", err)
	}
	if amt == nil || *amt != -0.77 {
		t.Fatalf("open leg commission cashflow = %v, want −0.77（入场费入账）", amt)
	}
	// 幂等键：同日同腿 commission 已由 HasCashflowForLegDay 语义覆盖（重试路径 insertCashflowIdempotent 拦截）。
	has, err := s.HasCashflowForLegDay(ctx, posID, 0, "commission", time.Now().UTC().Truncate(24*time.Hour))
	if err != nil || !has {
		t.Fatalf("HasCashflowForLegDay=%v err=%v, want true（幂等键同日腿生效）", has, err)
	}
}

// TestD422_R1_OpenCloseFeeCoexist R1 锚：同日同腿 OPEN 费（tag="open"）与 CLOSE 佣金
// （tag=""）双落不互吞；各自重试幂等不双计；quality='actual'（D-425 实采值）。
func TestD422_R1_OpenCloseFeeCoexist(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_EXEC_TEST_PG_DSN")
	s := testStore(t)
	conn := seedConn(t, dsn)
	ctx := context.Background()
	var posID int64
	if err := conn.QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('opp-d422r1','XASSET_CARRY','OPEN',0) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed pos: %v", err)
	}
	p := &Pipeline{store: s}
	// OPEN 费落账
	legs := []LegSpec{{VenueKey: "okx:perp", VenueSymbol: "X", Side: "BUY", Qty: 1, PriceExpected: 100}}
	results := []LegResult{{VenueKey: "okx:perp", OrderID: "o1", Price: 100, Qty: 1, Status: "FILLED", FeeUSD: 0.30}}
	if err := p.persistOpenLegs(ctx, "req-d422r1", posID, legs, results); err != nil {
		t.Fatalf("persistOpenLegs: %v", err)
	}
	// CLOSE 佣金同日落账（R1 前会被腿日幂等键吞掉）
	// D-013：CLOSE 腿行落库→腿键 commission（leg:<id>，与 OPEN 费行分键双落）
	closeFee := 0.50
	closeLeg := pgstore.PositionLeg{PositionID: posID, LegNo: 0, Phase: "CLOSE", AttemptNo: 1,
		Venue: "okx:perp", Symbol: "X", Side: "SELL", Qty: 1, Status: "FILLED", FeeUSD: &closeFee}
	if err := s.CreatePositionLeg(ctx, &closeLeg); err != nil {
		t.Fatalf("create close leg: %v", err)
	}
	if err := p.writeCloseCashflows(ctx, posID, closeLeg, 0, 0, 0.50, 1.0); err != nil {
		t.Fatalf("writeCloseCashflows: %v", err)
	}
	// 两条都应在：腿键分键（open 腿 id vs close 腿 id）
	var cnt int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM position_cashflows WHERE position_id=$1 AND kind='commission'`, posID).Scan(&cnt); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 2 {
		t.Fatalf("commission rows=%d, want 2（OPEN 费 + CLOSE 佣金腿键分键双落）", cnt)
	}
	var keyN int
	if err := conn.QueryRow(ctx,
		`SELECT count(DISTINCT event_key) FROM position_cashflows WHERE position_id=$1 AND kind='commission' AND event_key LIKE 'leg:%'`, posID).Scan(&keyN); err != nil {
		t.Fatalf("key count: %v", err)
	}
	if keyN != 2 {
		t.Fatalf("leg: 键分键失败：%d", keyN)
	}
	// quality='actual'（D-425 实采值入正式统计）
	var q string
	if err := conn.QueryRow(ctx,
		`SELECT DISTINCT quality FROM position_cashflows WHERE position_id=$1`, posID).Scan(&q); err != nil {
		t.Fatalf("quality: %v", err)
	}
	if q != "actual" {
		t.Fatalf("quality=%q want 'actual'（实采值，D-425）", q)
	}
	// 各自重试幂等：重发 CLOSE 佣金不双计
	if err := p.writeCloseCashflows(ctx, posID, closeLeg, 0, 0, 0.50, 1.0); err != nil {
		t.Fatalf("retry writeCloseCashflows: %v", err)
	}
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM position_cashflows WHERE position_id=$1 AND kind='commission'`, posID).Scan(&cnt); err != nil {
		t.Fatalf("count after retry: %v", err)
	}
	if cnt != 2 {
		t.Fatalf("retry 后 rows=%d, want 2（幂等不双计）", cnt)
	}
}
