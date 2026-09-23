package autotrade

// D-409 S5：收割费用摊销门槛——收割线命中后追加全账净额判定；净负+carry活+摊平有望
// → hold_fee_amortize 延迟收割（观测行即延迟时钟，首行起算不重置）。
import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// d409Truncate：position_evals/position_cashflows 不在 d311Truncate 清单，本 fixture 独占清。
func d409Truncate(t *testing.T, s *pgstore.Store) {
	t.Helper()
	d311Truncate(t, s)
	if _, err := s.Pool().Exec(context.Background(),
		`DELETE FROM position_evals; DELETE FROM fin_xasset_monitor WHERE pair='XPTUSDT-XPT'`); err != nil {
		t.Fatalf("d409 truncate: %v", err)
	}
}

// TestD409_Verdict 纯函数矩阵（S5 钉序）：carry 缺→carry 死→净非负→estDays 超限→时钟超限→hold。
func TestD409_Verdict(t *testing.T) {
	cap := 2.0
	cases := []struct {
		name       string
		net, carry float64
		carryOK    bool
		estDays    float64
		delaying   bool
		elapsed    time.Duration
		wantHold   bool
		wantFlag   string
	}{
		{"净负+carry活+estDays≤cap → hold", -0.27, 22.8, true, 0.17, false, 0, true, ""},
		{"estDays>cap → harvest(est_days_over)", -50, 10, true, 5.0, false, 0, false, "est_days_over"},
		{"carry死 → harvest", -1, -3, true, 0.1, false, 0, false, "carry_dead"},
		{"净额非负 → harvest", 1.5, 22.8, true, 0.1, false, 0, false, "net="},
		{"carry缺 → harvest(carry_unknown)", -1, 0, false, math.NaN(), false, 0, false, "carry_unknown"},
		{"延迟中超首行时钟 → harvest(forced)", -0.5, 22.8, true, 0.1, true, 73 * time.Hour, false, "forced"},
		{"延迟中未超时 → 继续 hold", -0.5, 22.8, true, 0.1, true, 24 * time.Hour, true, ""},
		{"延迟中 carry 转死 → 立即割", -0.5, -1, true, 0.1, true, time.Hour, false, "carry_dead"},
	}
	for _, tc := range cases {
		hold, flag := feeAmortizeVerdict(tc.net, tc.carry, tc.carryOK, tc.estDays, cap, tc.delaying, tc.elapsed)
		if hold != tc.wantHold || (tc.wantFlag != "" && !containsSub(flag, tc.wantFlag)) {
			t.Errorf("%s: hold=%v flag=%q", tc.name, hold, flag)
		}
	}
}

// TestD409_498Replay A5 数值重放：carry 2.79 + capture≈1.3 − fees 4.36 → net≈−0.27<0 → hold。
func TestD409_498Replay(t *testing.T) {
	net := 2.79 + 1.3 - 4.36 // estExit 项相对量级忽略（498 实差口径）
	if math.Abs(net-(-0.27)) > 0.02 {
		t.Fatalf("498 replay net=%.3f want ≈−0.27", net)
	}
	estDays := math.Abs(net) / (22.8 / 100 / 365 * 4000) // curNetCarry 22.8%/yr × 买腿名义 4000
	hold, flag := feeAmortizeVerdict(net, 22.8, true, estDays, 2.0, false, 0)
	if !hold || flag != "" {
		t.Fatalf("498 replay want hold_fee_amortize, hold=%v flag=%q", hold, flag)
	}
}

// d409Seed498Like 498 形态持仓：anchor=−12.39/floor=4.73/monitor 行 entry=7.73（capture=3，
// line=9.39）；双腿 FILLED @4000 qty=1（买腿名义 4000）；fee_usd 各 2.18（合计 4.36）；
// cashflow funding +2.79；carry facts：mt5 swap_long 5bps/d×3.65=18.25 + okx funding 4.55 =22.8%/yr。
// 报价 basis=−9.0（buyBid 3996.4/sellAsk 4000）→ 收割线命中 → gate 判 net≈−4.2<0 应 hold。
func d409Seed498Like(t *testing.T, s *pgstore.Store) *pgstore.Position {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	p := d404XAssetPos(t, s, d341fptr(-12.39), d341fptr(4.73))
	d404SeedMonitorRowAt(t, s, p.OpenedAt.Add(-5*time.Second), 7.73, 4.73)
	fee := 2.18
	if _, err := s.Pool().Exec(ctx,
		`UPDATE position_legs SET price_actual=4000, fee_usd=$2 WHERE position_id=$1`, p.ID, fee); err != nil {
		t.Fatalf("set fills+fees: %v", err)
	}
	if err := s.InsertCashflow(ctx, p.ID, 2, "funding", "okx", 2.79, now); err != nil {
		t.Fatalf("cashflow: %v", err)
	}
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "3001157", Symbol: "XPTUSD", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindFunding, Venue: "okx", Symbol: "XPTUSDT", Value: 4.55, Unit: fact.UnitPctAnnualized, Ts: now, Src: "okx"},
	}); err != nil {
		t.Fatalf("facts: %v", err)
	}
	rows, err := s.ListPositions(ctx, "OPEN", 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("relist: %v", err)
	}
	return &rows[0]
}

// TestD409_GateHold DSN 锚：498 形态 → feeAmortizeGate hold；xassetExitCheck 落
// hold_fee_amortize 观测行且不触发平仓；延迟时钟首行锚可重构。
func TestD409_GateHold(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d409Truncate(t, s)
	p := d409Seed498Like(t, s)
	qb := d404Quotes(time.Now().UTC(), 3996.4, 4000, 0) // basis=−9.0 ≥ line −9.39 → 收割线命中
	e := d404Engine(s, qb)
	e.cfg.XAssetCexFeeBps = 5.0
	e.cfg.XAssetFeeHoldMaxDays = 2.0
	e.cfg.XAssetExecDriftBps = 100 // 漂移闸抬阈（fill=0 vs anchor −12.39 本例恒漂，非本票断言对象）

	// gate 直测：hold + detail 五值齐
	pairKey, ok := e.xassetPairKey(ctx, p)
	if !ok {
		t.Fatal("pairKey")
	}
	hold, detail := e.feeAmortizeGate(ctx, p, pairKey)
	if !hold {
		t.Fatalf("gate want hold, detail=%q", detail)
	}
	for _, frag := range []string{"net=", "capture=", "fees=", "estExit=", "estDays="} {
		if !containsSub(detail, frag) {
			t.Errorf("detail 缺 %s: %q", frag, detail)
		}
	}

	// e2e：xassetExitCheck 不触发平仓 + 观测行 decision=hold_fee_amortize
	close, reason := e.xassetExitCheck(ctx, p)
	if close {
		t.Fatalf("fee-hold 不应平仓, reason=%q", reason)
	}
	var n int
	var dec, det string
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*), max(decision), max(detail) FROM position_evals WHERE position_id=$1`,
		p.ID).Scan(&n, &dec, &det); err != nil || n != 1 || dec != "hold_fee_amortize" {
		t.Fatalf("观测行 n=%d dec=%q err=%v", n, dec, err)
	}
	if !containsSub(det, "net=-") {
		t.Errorf("detail 应含负净额: %q", det)
	}
	// 时钟锚：首行 evaluated_at 可被 FirstFeeAmortizeEvalAt 重构
	if _, hit, err := s.FirstFeeAmortizeEvalAt(ctx, p.ID); err != nil || !hit {
		t.Fatalf("时钟锚 hit=%v err=%v", hit, err)
	}
}

// TestD409_GateHarvestPaths 放行路径：net≥0/estDays 超/时钟超/carry死/legacy/anchor>0。
func TestD409_GateHarvestPaths(t *testing.T) {
	ctx := context.Background()
	mk := func(t *testing.T, mutate func(s *pgstore.Store, p *pgstore.Position)) (*Engine, *pgstore.Position, string) {
		s := testAccumStore(t)
		d409Truncate(t, s)
		p := d409Seed498Like(t, s)
		mutate(s, p)
		e := d404Engine(s, d404Quotes(time.Now().UTC(), 3996.4, 4000, 0))
		e.cfg.XAssetCexFeeBps = 5.0
		e.cfg.XAssetFeeHoldMaxDays = 2.0
		e.cfg.XAssetExecDriftBps = 100
		k, _ := e.xassetPairKey(ctx, p)
		return e, p, k
	}
	_ = ctx

	// ①净额非负（fee 极小）→ harvest（gate 放行）
	e, p, k := mk(t, func(s *pgstore.Store, p *pgstore.Position) {
		s.Pool().Exec(context.Background(), `UPDATE position_legs SET fee_usd=0.01 WHERE position_id=$1`, p.ID)
	})
	if hold, d := e.feeAmortizeGate(context.Background(), p, k); hold {
		t.Errorf("①net≥0 应放行, detail=%q", d)
	}

	// ②estDays 超上限（巨额 fee）→ harvest est_days_over
	e, p, k = mk(t, func(s *pgstore.Store, p *pgstore.Position) {
		s.Pool().Exec(context.Background(), `UPDATE position_legs SET fee_usd=500 WHERE position_id=$1`, p.ID)
	})
	if hold, d := e.feeAmortizeGate(context.Background(), p, k); hold || !containsSub(d, "est_days_over") {
		t.Errorf("②应 est_days_over, hold=%v detail=%q", hold, d)
	}

	// ③延迟中超首行时钟 → harvest forced（先落 3 天前 hold_fee_amortize 行）
	e, p, k = mk(t, func(s *pgstore.Store, p *pgstore.Position) {
		s.InsertPositionEval(context.Background(), pgstore.PositionEval{PositionID: p.ID, Decision: "hold_fee_amortize", Detail: "seed"})
		s.Pool().Exec(context.Background(),
			`UPDATE position_evals SET evaluated_at=now()-interval '3 days' WHERE position_id=$1`, p.ID)
	})
	if hold, d := e.feeAmortizeGate(context.Background(), p, k); hold || !containsSub(d, "forced") {
		t.Errorf("③应 forced, hold=%v detail=%q", hold, d)
	}

	// ④carry 死（funding 转负巨额）→ harvest
	e, p, k = mk(t, func(s *pgstore.Store, p *pgstore.Position) {
		s.InsertFacts(context.Background(), []fact.Fact{
			{Kind: fact.KindFunding, Venue: "okx", Symbol: "XPTUSDT", Value: -100, Unit: fact.UnitPctAnnualized, Ts: time.Now().UTC(), Src: "okx"},
		})
	})
	if hold, d := e.feeAmortizeGate(context.Background(), p, k); hold || !containsSub(d, "carry_dead") {
		t.Errorf("④应 carry_dead, hold=%v detail=%q", hold, d)
	}

	// ⑤代理路径（monitor 行缺 → found=false）→ 不挂门槛 legacy_path
	e, p, k = mk(t, func(s *pgstore.Store, p *pgstore.Position) {
		s.Pool().Exec(context.Background(), `DELETE FROM fin_xasset_monitor WHERE pair='XPTUSDT-XPT'`)
	})
	if hold, d := e.feeAmortizeGate(context.Background(), p, k); hold || d != "legacy_path" {
		t.Errorf("⑤应 legacy_path, hold=%v detail=%q", hold, d)
	}

	// ⑥anchor>0 域 → 不介入（detail 空）
	e, p, k = mk(t, func(s *pgstore.Store, p *pgstore.Position) {
		p.ExpectedEntrySpreadBps = d341fptr(12.39) // 内存态同步（gate 读 p 字段非重查库）
	})
	if hold, d := e.feeAmortizeGate(context.Background(), p, k); hold || d != "" {
		t.Errorf("⑥anchor>0 不介入, hold=%v detail=%q", hold, d)
	}

	// ⑦过冲域符号（R0-B2）：basis=+5（buyBid 4020 → basis=+50bps…用 buyBid=4002 → +5bps）
	// anchor=−12.39 → capture=+17.39bps（绝对值式会得 7.39 低估）。净额含过冲收敛 → 放行。
	s7 := testAccumStore(t)
	d409Truncate(t, s7)
	p7 := d409Seed498Like(t, s7)
	e7 := d404Engine(s7, d404Quotes(time.Now().UTC(), 4002.0, 4000, 0))
	e7.cfg.XAssetCexFeeBps = 5.0
	e7.cfg.XAssetFeeHoldMaxDays = 2.0
	e7.cfg.XAssetExecDriftBps = 100
	k7, _ := e7.xassetPairKey(context.Background(), p7)
	// basis=+5bps：capture=(5−(−12.39))×4000/1e4=+6.96；net=2.79+6.96−4.36−4.0=+1.39≥0 → harvest
	hold, d := e7.feeAmortizeGate(context.Background(), p7, k7)
	if hold || !containsSub(d, "net=") {
		t.Errorf("⑦过冲 net≥0 应放行, hold=%v detail=%q", hold, d)
	}
	if !containsSub(d, "net=1.3") && !containsSub(d, "net=1.4") {
		t.Errorf("⑦过冲 net 应≈+1.39（差值式），detail=%q", d)
	}
}

// TestD409_LedgerFeeEstimate fee_usd NULL 腿按 xassetCexFeeBps 估算（enrich 同族，禁静默零值）。
func TestD409_LedgerFeeEstimate(t *testing.T) {
	price := 4000.0
	fee := 2.0
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Side: "BUY", Qty: 1, PriceActual: &price, FeeUSD: &fee, Venue: "v:mt5", Symbol: "X"},
		{Phase: "OPEN", Status: "FILLED", Side: "SELL", Qty: 1, PriceActual: &price, Venue: "okx:perp", Symbol: "Y"},
	}
	buyN, paid, exit, ok := xassetLegFeeLedger(legs, nil, 5.0)
	if !ok || buyN != 4000 {
		t.Fatalf("buyNotional=%v ok=%v", buyN, ok)
	}
	// paid=2.0 实值 + 4000×5bps/1e4=2.0 估算 = 4.0；exit=两腿各 2.0=4.0
	if paid != 4.0 || exit != 4.0 {
		t.Errorf("paid=%.2f exit=%.2f want 4.00/4.00", paid, exit)
	}
	// 缺价腿 → ok=false（缺数据不静默零值）
	legs[1].PriceActual = nil
	if _, _, _, ok := xassetLegFeeLedger(legs, nil, 5.0); ok {
		t.Error("缺价腿应 ok=false")
	}
}
