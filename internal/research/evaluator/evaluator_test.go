package evaluator

import (
	"testing"
	"time"

	"arb/internal/research/detector"
)

func baseCfg() Config {
	return Config{
		SlippageBps:          1.0,
		MarginRateAnnual:     4.5,
		DetectorFreshnessTTL: 60 * time.Second,
		OpportunityTTL:       30 * time.Second,
		GrabMinNetAnnualized: 4.5,
	}
}

// TestEvaluateCrossVenueRoundTripSpread D-113：往返 bid-ask 穿透成本验证。
// spreadBuy/spreadSell = 原始价格单位 half-spread → ×4 = 往返全宽 spread（开仓+平仓各吃一次 bid-ask 穿透）。
// EURUSD half-spread=0.0001（1 pip 真实值）/ mid 1.1006 × 40000 = 7.27 bps → net = 8.18 - 7.27 - 5.2 = -4.29 → trap。
func TestEvaluateCrossVenueRoundTripSpread(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "EURUSD",
		VenueBuy:  "a:mt4",
		VenueSell: "b:mt4",
		PriceBuy:  1.1001,
		PriceSell: 1.1010,
		GrossBps:  8.18, // 检测器用可执行价算：开仓方向 spread 已含
		QuoteTime: time.Now().UnixMilli(),
	}
	r := Evaluate(c, baseCfg(), 0.0001, 0.0001, 0.6, 0)
	if r == nil {
		t.Fatal("nil result")
	}
	// 往返 bid-ask 7.27 bps + round-trip commission+slippage 5.2 bps = 12.47 > gross 8.18 → trap
	if r.Rating != RatingTrap {
		t.Fatalf("Rating = %s, want trap (round-trip spread+cost > gross)", r.Rating)
	}
	if r.NetBps >= 0 {
		t.Fatalf("NetBps = %v, want < 0 (D-113 往返 bid-ask 穿透成本)", r.NetBps)
	}
	if r.BreakevenDays != nil {
		t.Fatal("CrossVenue should not have breakeven_days")
	}
}

// TestEvaluateCarry 息差型：净年化 = gross − margin_rate，breakeven 用净年化推日息差（D-036）。
func TestEvaluateCarry(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCarry,
		Canonical: "EURUSD",
		VenueBuy:  "a:mt5",
		VenueSell: "b:mt5",
		GrossBps:  10.0, // 年化百分点（D-036 单位统一）
		QuoteTime: time.Now().UnixMilli(),
	}
	cfg := baseCfg()
	cfg.GrabMinNetAnnualized = 4.0
	r := Evaluate(c, cfg, 0.5, 0.5, 0.6, 0)
	if r == nil {
		t.Fatal("nil result")
	}
	// net = 10 - 4.5 = 5.5%
	if r.NetBps < 5.4 || r.NetBps > 5.6 {
		t.Fatalf("NetBps = %v, want ~5.5（gross 10 − margin 4.5）", r.NetBps)
	}
	if r.BreakevenDays == nil {
		t.Fatal("Carry should have breakeven_days")
	}
	// dailyNetBps = 5.5/100×10⁴/365 ≈ 1.507; oneTime = (1.0+0.6+2.0)*2 = 7.2; bd ≈ 4.78
	if *r.BreakevenDays < 4.6 || *r.BreakevenDays > 5.0 {
		t.Fatalf("BreakevenDays = %v, want ~4.78 (round-trip cost)", *r.BreakevenDays)
	}
}

// TestEvaluateCarryMarginDeducted carry 净收益扣资金机会成本（与 funding 一致，D-036）。
func TestEvaluateCarryMarginDeducted(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCarry,
		Canonical: "X",
		VenueBuy:  "a:mt5",
		VenueSell: "b:mt5",
		GrossBps:  4.0, // 刚过 carry 门槛 4.0，扣 4.5 margin 后为负
		QuoteTime: time.Now().UnixMilli(),
	}
	cfg := baseCfg()
	cfg.GrabMinNetAnnualized = 4.0
	r := Evaluate(c, cfg, 0, 0, 0, 0)
	// gross 4.0 − margin 4.5 = −0.5 → 恒为 trap（不进任何视图）
	if r.NetBps >= 0 {
		t.Fatalf("NetBps = %v, want < 0（margin 未扣则净收益虚高）", r.NetBps)
	}
	if r.Rating != RatingTrap {
		t.Fatalf("Rating = %s, want trap（net 4.0-4.5=-0.5 ≤ 0）", r.Rating)
	}
}

// TestEvaluateTrap 负净收益恒为 trap（不进任何视图）。
// gross=1.0 远小于往返 bid-ask 7.27 + round-trip 摩擦 5.2 = 12.47 → net 大幅为负。
func TestEvaluateTrap(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "X",
		VenueBuy:  "a:mt4",
		VenueSell: "b:mt4",
		PriceBuy:  1.1001,
		PriceSell: 1.1010,
		GrossBps:  1.0, // 1.0 - 7.27 - 5.2 < 0
		QuoteTime: time.Now().UnixMilli(),
	}
	r := Evaluate(c, baseCfg(), 0.0001, 0.0001, 0.6, 0)
	if r.Rating != RatingTrap {
		t.Fatalf("Rating = %s, want trap (net <= 0)", r.Rating)
	}
}

// TestEvaluateCrossVenueRoundTrip D-078+D-113：round-trip 成本（commission+slippage×2 + 往返 bid-ask）验证。
// spread 0.0001+0.0001 / mid 1.1006 × 40000 = 7.27 bps 往返 bid-ask + (0.6+2.0)*2=5.2 bps round-trip 摩擦 = 12.47 bps 总成本。
// gross=5 → net=-7.47 trap；gross=10 → net=-2.47 trap；gross=50 → net=37.53 grab（大 gross 仍可盈利）。
func TestEvaluateCrossVenueRoundTrip(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "EURUSD",
		VenueBuy:  "a:mt4",
		VenueSell: "b:mt4",
		PriceBuy:  1.1001,
		PriceSell: 1.1010,
		GrossBps:  5.0, // 5.0 - 7.27 - 5.2 = -7.47 → trap
		QuoteTime: time.Now().UnixMilli(),
	}
	r := Evaluate(c, baseCfg(), 0.0001, 0.0001, 0.6, 0)
	if r.Rating != RatingTrap {
		t.Fatalf("Rating = %s, want trap (gross 5bps < round-trip cost 12.47bps)", r.Rating)
	}
	// gross=10 仍 < 12.47 → trap（旧 D-078 模型只扣 5.2 → grab，bid-ask 穿透漏算）
	c.GrossBps = 10.0
	r2 := Evaluate(c, baseCfg(), 0.0001, 0.0001, 0.6, 0)
	if r2.Rating != RatingTrap {
		t.Fatalf("Rating = %s, want trap (gross 10bps < round-trip+bid-ask 12.47bps)", r2.Rating)
	}
	// gross=50 → net = 50 - 7.27 - 5.2 = 37.53 → grab（大 gross 仍可盈利，验证公式非恒 trap）
	c.GrossBps = 50.0
	r3 := Evaluate(c, baseCfg(), 0.0001, 0.0001, 0.6, 0)
	if r3.Rating != RatingGrab {
		t.Fatalf("Rating = %s, want grab (gross 50bps > round-trip+bid-ask 12.47bps)", r3.Rating)
	}
	if r3.NetBps < 37.0 || r3.NetBps > 38.0 {
		t.Fatalf("NetBps = %v, want ~37.5", r3.NetBps)
	}
}

// TestEvaluateCrossVenueWideSpread D-113：宽 spread venue 组合（XPDUSD 实证场景）。
// REDACTED_MT_ACCT bid-ask $4.26 极宽 → spreadSell=2.13；39050818 spreadBuy=0.735。
// mid=1338.2, roundTripSpreadBps = (0.735+2.13)/1338.2*10000*4 ≈ 85.7 bps。
// net = 23.67 - 85.7 - 3.6 ≈ -65.6 → trap（实证 51 笔只 1 盈累计 -$512 的根因）。
func TestEvaluateCrossVenueWideSpread(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "XPDUSD",
		VenueBuy:  "39050818:mt5",
		VenueSell: "REDACTED_MT_ACCT:mt5",
		PriceBuy:  1337.42,
		PriceSell: 1338.98,
		GrossBps:  23.67,
		QuoteTime: time.Now().UnixMilli(),
	}
	// spreadBuy = (1337.42-1335.95)/2 = 0.735, spreadSell = (1343.24-1338.98)/2 = 2.13
	r := Evaluate(c, baseCfg(), 0.735, 2.13, 0.6, 0)
	if r == nil {
		t.Fatal("nil result")
	}
	if r.Rating != RatingTrap {
		t.Fatalf("Rating = %s, want trap (宽 spread venue 组合 net 大幅为负)", r.Rating)
	}
	if r.NetBps >= -60 {
		t.Fatalf("NetBps = %v, want < -60 (宽 spread 往返 bid-ask 85.7 bps)", r.NetBps)
	}
}

// TestEvaluateCrossVenueZeroSpread D-113：spread=0 回退 D-078 行为（无 bid-ask 穿透成本）。
// spreadBuy=spreadSell=0 → roundTripSpreadBps=0 → net = 8.18 - (0.6+2.0)*2 = 2.98 → grab。
func TestEvaluateCrossVenueZeroSpread(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "EURUSD",
		VenueBuy:  "a:mt4",
		VenueSell: "b:mt4",
		PriceBuy:  1.1001,
		PriceSell: 1.1010,
		GrossBps:  8.18,
		QuoteTime: time.Now().UnixMilli(),
	}
	r := Evaluate(c, baseCfg(), 0, 0, 0.6, 0)
	if r == nil {
		t.Fatal("nil result")
	}
	if r.Rating != RatingGrab {
		t.Fatalf("Rating = %s, want grab (spread=0 回退 D-078)", r.Rating)
	}
	// net = 8.18 - 0 - (0.6+2.0)*2 = 2.98
	if r.NetBps < 2.8 || r.NetBps > 3.2 {
		t.Fatalf("NetBps = %v, want ~2.98 (spread=0 回退 D-078 round-trip)", r.NetBps)
	}
}

// TestClassify_Avg30dZero_DowngradeToBreakeven D-159 W4a S5a：avg30dBps=0, netBps>grabThr → Breakeven（旧 Grab）。
// SSOT §5 Phase 3-MT 第 4 点：无历史基线时降级为 V0/观察，不直接进入自动交易。
func TestClassify_Avg30dZero_DowngradeToBreakeven(t *testing.T) {
	got := classify(detector.TypeCarry, 10.0, 10.0, 0, 5.0)
	if got != RatingBreakeven {
		t.Errorf("avg30dBps=0 netBps=10>grabThr=5: got %s, want RatingBreakeven（降级 V0/观察）", got)
	}
}

// TestClassify_Avg30dPositive_InstantGt2x_Grab D-159 W4a S5a：avg30dBps=5, instantBps=11 → Grab。
func TestClassify_Avg30dPositive_InstantGt2x_Grab(t *testing.T) {
	got := classify(detector.TypeCarry, 10.0, 11.0, 5.0, 5.0)
	if got != RatingGrab {
		t.Errorf("avg30dBps=5 instantBps=11>10: got %s, want RatingGrab", got)
	}
}

// TestClassify_Avg30dPositive_InstantLt2x_Breakeven D-159 W4a S5a：avg30dBps=5, instantBps=9 → Breakeven。
func TestClassify_Avg30dPositive_InstantLt2x_Breakeven(t *testing.T) {
	got := classify(detector.TypeCarry, 10.0, 9.0, 5.0, 5.0)
	if got != RatingBreakeven {
		t.Errorf("avg30dBps=5 instantBps=9<10: got %s, want RatingBreakeven", got)
	}
}

// TestEvaluateAt_DeterministicExpiresAt D-194 §3.3：同一 now → 同一 ExpiresAt；不同 now → 不同 ExpiresAt。
func TestEvaluateAt_DeterministicExpiresAt(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "EURUSD",
		VenueBuy:  "a:mt4",
		VenueSell: "b:mt4",
		PriceBuy:  1.1001,
		PriceSell: 1.1010,
		GrossBps:  8.18,
		QuoteTime: time.Now().UnixMilli(),
	}
	cfg := baseCfg()
	now1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now2 := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	r1 := EvaluateAt(c, cfg, 0.0001, 0.0001, 0.6, 0, now1)
	r2 := EvaluateAt(c, cfg, 0.0001, 0.0001, 0.6, 0, now1) // 同 now
	r3 := EvaluateAt(c, cfg, 0.0001, 0.0001, 0.6, 0, now2) // 不同 now
	if !r1.ExpiresAt.Equal(r2.ExpiresAt) {
		t.Errorf("同 now ExpiresAt 不一致: r1=%v r2=%v", r1.ExpiresAt, r2.ExpiresAt)
	}
	if r1.ExpiresAt.Equal(r3.ExpiresAt) {
		t.Errorf("不同 now ExpiresAt 相同: r1=%v r3=%v", r1.ExpiresAt, r3.ExpiresAt)
	}
	want1 := now1.Add(cfg.OpportunityTTL)
	if !r1.ExpiresAt.Equal(want1) {
		t.Errorf("ExpiresAt=%v, want %v (now+TTL)", r1.ExpiresAt, want1)
	}
}

// TestEvaluate_CompatWrapper D-194 §3.3：Evaluate 与 EvaluateAt(..., time.Now()) 行为一致。
func TestEvaluate_CompatWrapper(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "EURUSD",
		VenueBuy:  "a:mt4",
		VenueSell: "b:mt4",
		PriceBuy:  1.1001,
		PriceSell: 1.1010,
		GrossBps:  8.18,
		QuoteTime: time.Now().UnixMilli(),
	}
	cfg := baseCfg()
	before := time.Now()
	rEval := Evaluate(c, cfg, 0.0001, 0.0001, 0.6, 0)
	after := time.Now()
	// ExpiresAt 应在 before+TTL ~ after+TTL 之间（time.Now() 精度内）
	minExpires := before.Add(cfg.OpportunityTTL)
	maxExpires := after.Add(cfg.OpportunityTTL)
	if rEval.ExpiresAt.Before(minExpires) || rEval.ExpiresAt.After(maxExpires) {
		t.Errorf("Evaluate ExpiresAt=%v 不在 [%v, %v] 范围内（应为 time.Now()+TTL）", rEval.ExpiresAt, minExpires, maxExpires)
	}
}
