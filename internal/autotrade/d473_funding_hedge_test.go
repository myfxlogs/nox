package autotrade

// D-473 验收锚：S1 FUNDING_HEDGE 组合白名单闸 + S3 regime 闸/正构型钉死。
// SSOT=docs/D-473设计与施工提示词-*.md §S1/S3/S5。
//
// 观测点=d330 同型：FUNDING_HEDGE 标 disabled → intent 产出即被 gate 早拒落
// REJECTED 行（1 行=过全部闸；0 行=被中途静默 skip）。

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

func d473Setup(t *testing.T) *pgstore.Store {
	t.Helper()
	s := d330Setup(t) // RUNNING 状态
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `INSERT INTO strategy_stats (strategy, disabled) VALUES ('FUNDING_HEDGE', true)`); err != nil {
		t.Fatalf("seed disabled: %v", err)
	}
	return s
}

// d473SeedFunding 种 okx 正 funding 样本（S3 regime 闸通过态）。
func d473SeedFunding(t *testing.T, s *pgstore.Store, value float64) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 13; i++ {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
			 VALUES ('funding', 'okx', 'SKLUSDT', $1, 'pct_annualized', $2, 'd473test')`,
			value, time.Now().Add(-time.Duration(13-i)*5*time.Minute)); err != nil {
			t.Fatalf("seed funding %d: %v", i, err)
		}
	}
}

// d473Engine FUNDING_HEDGE 引擎：白名单 spec + regime 配置 + spot/perp 报价。
func d473Engine(s *pgstore.Store, spec *exec.XAssetComboAllowSpec) *Engine {
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "SKLUSDT", Venue: "okx", VenueType: "spot", Bid: 0.49, Ask: 0.50, Time: time.Now(), VenueSymbol: "SKL-USDT"})
	qb.Publish(bus.Quote{Symbol: "SKLUSDT", Venue: "okx", VenueType: "perp", Bid: 0.50, Ask: 0.51, Time: time.Now(), VenueSymbol: "SKL-USDT-SWAP"})
	svc := dashboard.New(nil, qb, nil, nil, nil, 0, 0, 0, 0, 0, 0, nil, nil, nil, nil, 0, nil, nil, 0, 0, nil)
	svc.SetLegContractSize(func(venueKey, venueSymbol string) (float64, bool) {
		return 1.0, true // :spot cs=1.0
	})
	svc.SetCexContractSize(func(venue, venueSymbol string) (float64, bool) {
		return 1.0, true // :perp ct_val=1
	})
	cfg := Config{
		MinNetBps:   0,
		NotionalUSD: 100,
		AllowVenues: map[string]bool{"okx:spot": true, "okx:perp": true},
		CarryRegime: exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12},
		EconFeeBps:  d014FeeAny, // D-014 入场闸费率源（fake：CEX 5bps）
	}
	if spec != nil {
		cfg.XAssetAllowCombos = []exec.XAssetComboAllowSpec{*spec}
	}
	return &Engine{
		store: s, service: svc, quotes: qb, gate: NewGate(s, Config{}, nil),
		csMissCounts: map[string]int{}, csMissSent: map[string]time.Time{},
		cfg: cfg,
	}
}

func d473Opp(buyVenue, sellVenue string) pgstore.Opportunity {
	return pgstore.Opportunity{
		ID: "FUNDING_HEDGE:SKLUSDT:" + buyVenue + ":" + sellVenue, Type: "FUNDING_HEDGE",
		Canonical: "SKLUSDT", VenueBuy: buyVenue, VenueSell: sellVenue, NetBps: 10,
	}
}

var d473Spec = exec.XAssetComboAllowSpec{
	CanonicalBuy: "SKLUSDT", CanonicalSell: "SKLUSDT",
	VenueBuy: "okx:spot", VenueSell: "okx:perp",
	MaxLongQty: 10, MaxShortQty: 10, MaxGrossUSD: 10000,
}

// S1 锚①：spec 命中 + regime 合格 → 产 intent（disabled 预拒落 1 行）。
func TestD473_ComboGate_AllowlistPass(t *testing.T) {
	s := d473Setup(t)
	d006LiftCanonicalCap(t, s, "SKLUSDT")
	d473SeedFunding(t, s, 50.0) // D-014：回本闸需 carry 覆盖费用（$0.10 成本 → BE≈0.15d）
	e := d473Engine(s, &d473Spec)
	opp := d473Opp("okx:spot", "okx:perp")
	e.handleOpportunity(context.Background(), opp, newComboAccumulator())
	if n := d330CountLogs(t, s, opp.ID); n != 1 {
		t.Fatalf("spec 命中+regime 合格应产 intent（1 行 REJECTED）, got %d 行", n)
	}
}

// S1 锚②：无 allow_combos 行 → 静默 skip（0 行）。
func TestD473_ComboGate_NoRowSkips(t *testing.T) {
	s := d473Setup(t)
	d473SeedFunding(t, s, 5.0)
	e := d473Engine(s, nil) // 空白名单=全拒
	opp := d473Opp("okx:spot", "okx:perp")
	e.handleOpportunity(context.Background(), opp, newComboAccumulator())
	if n := d330CountLogs(t, s, opp.ID); n != 0 {
		t.Fatalf("无组合白名单应静默 skip, got %d 行", n)
	}
}

// S3 锚①：反构型（perp 买×spot 卖=借货路径）→ 拒（spec 匹配也拦）。
func TestD473_ReverseConfigRejected(t *testing.T) {
	s := d473Setup(t)
	d473SeedFunding(t, s, 5.0)
	spec := exec.XAssetComboAllowSpec{
		CanonicalBuy: "SKLUSDT", CanonicalSell: "SKLUSDT",
		VenueBuy: "okx:perp", VenueSell: "okx:spot",
		MaxLongQty: 10, MaxShortQty: 10, MaxGrossUSD: 10000,
	}
	e := d473Engine(s, &spec)
	opp := d473Opp("okx:perp", "okx:spot")
	e.handleOpportunity(context.Background(), opp, newComboAccumulator())
	if n := d330CountLogs(t, s, opp.ID); n != 0 {
		t.Fatalf("反构型应拒开（借货路径不入）, got %d 行", n)
	}
}

// S3 锚②（D-014 改写）：funding 全负 → CarryPerDay≤0 → 回本无期（BreakevenDays=+∞>3）
// econ 入场闸拒开——旧「latest≤0 豁免放行产 intent」语义被 D-014 取代
// （负 carry 的 FUNDING_HEDGE 本就无收益来源，拒开是设计终态）。
func TestD473_RegimeGate_NegativeLatestNonIncomeExempt(t *testing.T) {
	s := d473Setup(t)
	d006LiftCanonicalCap(t, s, "SKLUSDT")
	d473SeedFunding(t, s, -5.0)
	e := d473Engine(s, &d473Spec)
	opp := d473Opp("okx:spot", "okx:perp")
	e.handleOpportunity(context.Background(), opp, newComboAccumulator())
	if n := d330CountLogs(t, s, opp.ID); n != 0 {
		t.Fatalf("负 carry 应被 econ 回本闸拒开（CarryPerDay≤0→BE=+∞>3）, got %d 行", n)
	}
}

// S3 锚③：窗内塌落（latest 正但 worst<floor）→ 拒。
func TestD473_RegimeGate_RejectsDecayed(t *testing.T) {
	s := d473Setup(t)
	// 12 个正样本 + 序列内夹负值但 latest 必须正（否则豁免放行）——
	// requiredSign×latest>0 且 worst<floor → gate=false 拒。
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
			 VALUES ('funding', 'okx', 'SKLUSDT', $1, 'pct_annualized', $2, 'd473test')`,
			5.0, time.Now().Add(-time.Duration(12-i)*5*time.Minute)); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	// 塌落样本（min=-3 < floor 2）
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
		 VALUES ('funding', 'okx', 'SKLUSDT', -3.0, 'pct_annualized', $1, 'd473test')`,
		time.Now().Add(-6*time.Minute)); err != nil {
		t.Fatalf("seed decayed: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
		 VALUES ('funding', 'okx', 'SKLUSDT', 5.0, 'pct_annualized', $1, 'd473test')`,
		time.Now().Add(-1*time.Minute)); err != nil {
		t.Fatalf("seed latest: %v", err)
	}
	e := d473Engine(s, &d473Spec)
	opp := d473Opp("okx:spot", "okx:perp")
	e.handleOpportunity(context.Background(), opp, newComboAccumulator())
	if n := d330CountLogs(t, s, opp.ID); n != 0 {
		t.Fatalf("窗内塌落应拒开（worst<floor）, got %d 行", n)
	}
}

// S3 锚③：样本不足（facts 零行）→ fail-closed 拒。
func TestD473_RegimeGate_InsufficientSamples(t *testing.T) {
	s := d473Setup(t)
	e := d473Engine(s, &d473Spec)
	opp := d473Opp("okx:spot", "okx:perp")
	e.handleOpportunity(context.Background(), opp, newComboAccumulator())
	if n := d330CountLogs(t, s, opp.ID); n != 0 {
		t.Fatalf("零样本应拒开（未知 regime 不放行）, got %d 行", n)
	}
}
