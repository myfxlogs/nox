package review

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// fakeStore 实现 Store 接口的测试替身。
type fakeStore struct {
	positions []pgstore.Position
	legs      map[int64][]pgstore.PositionLeg
	cashflows map[int64][]pgstore.Cashflow
	upserts   []*pgstore.KnowledgeEntry
	// D-126 P2：csByBrokerSymbol[(venue,brokerSymbol)] = contract_size；未配置默认 1。
	csByBrokerSymbol map[[2]string]float64
}

func (f *fakeStore) ListPositions(ctx context.Context, status string, limit, offset int) ([]pgstore.Position, error) {
	var out []pgstore.Position
	for _, p := range f.positions {
		if p.Status == status {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error) {
	return f.legs[posID], nil
}

func (f *fakeStore) ListCashflows(ctx context.Context, posID int64) ([]pgstore.Cashflow, error) {
	return f.cashflows[posID], nil
}

func (f *fakeStore) UpsertKnowledgeEntry(ctx context.Context, e *pgstore.KnowledgeEntry) (int64, error) {
	f.upserts = append(f.upserts, e)
	return int64(len(f.upserts)), nil
}

// D-126 P2：fakeStore 实现 ContractSizeByBrokerSymbol。
// 未配置 csByBrokerSymbol 时返回 ok=false → notionalFromLegs 回退 cs=1（CEX 语义）。
func (f *fakeStore) ContractSizeByBrokerSymbol(ctx context.Context, venueKey, brokerSymbol string) (float64, bool, error) {
	if f.csByBrokerSymbol == nil {
		return 0, false, nil
	}
	cs, ok := f.csByBrokerSymbol[[2]string{venueKey, brokerSymbol}]
	return cs, ok, nil
}

func floatPtr(v float64) *float64    { return &v }
func timePtr(t time.Time) *time.Time { return &t }

func mkPos(id int64, typ string, expectedNetBps float64, realized float64, opened, closed time.Time) pgstore.Position {
	return pgstore.Position{
		ID: id, Type: typ, Status: "CLOSED", ExpectedNetBps: expectedNetBps,
		RealizedPnl: floatPtr(realized), OpenedAt: timePtr(opened), ClosedAt: timePtr(closed),
	}
}

func TestReviewPerPositionAndSummary(t *testing.T) {
	opened := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	closed30 := opened.Add(30 * 24 * time.Hour)

	// 息差型 3 笔：expected 年化 5% (500bps) × 持有30/365 → expected_holding_bps≈41.10
	// notional = 0.1×100000 = 10000，1bps=1USD
	f := &fakeStore{
		positions: []pgstore.Position{
			mkPos(1, "CARRY", 500, 40.0, opened, closed30),
			mkPos(2, "CARRY", 500, 41.0, opened, closed30),
			mkPos(3, "CARRY", 500, 39.0, opened, closed30),
		},
		legs: map[int64][]pgstore.PositionLeg{
			1: {{Side: "BUY", Symbol: "BTCUSD", Venue: "icmarkets", Qty: 0.1, PriceExpected: 100000}},
			2: {{Side: "BUY", Symbol: "BTCUSD", Venue: "icmarkets", Qty: 0.1, PriceExpected: 100000}},
			3: {{Side: "BUY", Symbol: "BTCUSD", Venue: "icmarkets", Qty: 0.1, PriceExpected: 100000}},
		},
		cashflows: map[int64][]pgstore.Cashflow{
			1: {{Kind: "swap", AmountUSD: 50}}, // 实收 = 40+50 = 90 > expected_holding 41.10
			2: {{Kind: "swap", AmountUSD: 0}},
			3: {{Kind: "swap", AmountUSD: 0}},
		},
	}

	cfg := Config{MinSamples: 3, CrossBiasBps: 1.0, CarryBiasAnnualPct: 1.0}
	if err := Run(context.Background(), f, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 每笔都写 review:pos:N
	if len(f.upserts) != 4 { // 3 pos + 1 summary
		t.Fatalf("期望 4 条 upsert，实际 %d", len(f.upserts))
	}
	got := map[string]*pgstore.KnowledgeEntry{}
	for _, e := range f.upserts {
		got[e.Signature] = e
	}
	// 笔 1：实收 90/10000×1e4=90bps，expected_holding=500×30/365=41.10 → deviation≈+48.9 → better
	if e := got["review:pos:1"]; e == nil || e.Verdict != "better_than_expected" {
		t.Errorf("pos:1 verdict=%q（期望 better_than_expected），rationale=%q", e.Verdict, e.Rationale)
	}
	// summary 存在且提到样本
	s := got["review:summary:CARRY"]
	if s == nil {
		t.Fatal("缺 review:summary:CARRY")
	}
	if s.Verdict != "tuning" {
		t.Errorf("summary verdict=%q（期望 tuning）", s.Verdict)
	}
	if s.Source != "review" {
		t.Errorf("summary source=%q（期望 review）", s.Source)
	}
}

func TestReviewBelowMinSamplesNoSummary(t *testing.T) {
	opened := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	closed := opened.Add(30 * 24 * time.Hour)
	f := &fakeStore{
		positions: []pgstore.Position{
			mkPos(1, "CARRY", 500, 40.0, opened, closed),
		},
		legs: map[int64][]pgstore.PositionLeg{
			1: {{Side: "BUY", Symbol: "BTCUSD", Venue: "icmarkets", Qty: 0.1, PriceExpected: 100000}},
		},
	}
	cfg := Config{MinSamples: 3, CrossBiasBps: 1.0, CarryBiasAnnualPct: 1.0}
	if err := Run(context.Background(), f, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.upserts) != 1 {
		t.Fatalf("样本不足应只有 1 条 pos 条目，实际 %d", len(f.upserts))
	}
	if f.upserts[0].Signature != "review:pos:1" {
		t.Errorf("signature=%q", f.upserts[0].Signature)
	}
}

func TestReviewCrossVenueHoldingBpsSnapshot(t *testing.T) {
	// 价差型：expected_holding = expected_net_bps 快照（一次性，不按持有期折算）。
	// expected_net_bps=20，持 30 天仍按 20 算；notional=10000 → 实收 20USD = 20bps → deviation=0 → spot_on
	opened := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	closed := opened.Add(30 * 24 * time.Hour)
	f := &fakeStore{
		positions: []pgstore.Position{
			mkPos(1, "CROSS_VENUE", 20, 20.0, opened, closed),
		},
		legs: map[int64][]pgstore.PositionLeg{
			1: {{Side: "BUY", Symbol: "EURUSD", Venue: "icmarkets", Qty: 10000, PriceExpected: 1.0}},
		},
		cashflows: map[int64][]pgstore.Cashflow{},
	}
	cfg := Config{MinSamples: 3, CrossBiasBps: 1.0, CarryBiasAnnualPct: 1.0}
	if err := Run(context.Background(), f, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.upserts) != 1 {
		t.Fatalf("期望 1 条，实际 %d", len(f.upserts))
	}
	e := f.upserts[0]
	if e.Verdict != "spot_on" {
		t.Errorf("verdict=%q（期望 spot_on，证明快照语义未按持有期折算），rationale=%q", e.Verdict, e.Rationale)
	}
	// 反例对照：若误按年化折算 expected_holding = 20×30/365≈0 → deviation≈+18.4 → better
	// 此处应验证签名正确（review:pos:1）
	if e.Signature != "review:pos:1" {
		t.Errorf("signature=%q", e.Signature)
	}
}

func TestReviewSkipsIncomplete(t *testing.T) {
	// realized_pnl / opened_at / closed_at 缺失 → 跳过，不归因
	opened := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	closed := opened.Add(30 * 24 * time.Hour)
	f := &fakeStore{
		positions: []pgstore.Position{
			{ID: 1, Type: "CARRY", Status: "CLOSED", ExpectedNetBps: 500, OpenedAt: timePtr(opened), ClosedAt: timePtr(closed)}, // realized nil
		},
		legs: map[int64][]pgstore.PositionLeg{
			1: {{Side: "BUY", Symbol: "BTCUSD", Venue: "icmarkets", Qty: 0.1, PriceExpected: 100000}},
		},
	}
	cfg := Config{MinSamples: 3, CrossBiasBps: 1.0, CarryBiasAnnualPct: 1.0}
	if err := Run(context.Background(), f, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.upserts) != 0 {
		t.Fatalf("缺回填应 0 条，实际 %d", len(f.upserts))
	}
}

// D-126 P2：验证 notionalFromLegs 乘 contract_size 后 deviation 正确。
// 场景：BRENT cs=1000, qty=1, price=80 → notional=80000（非旧 bug 的 80）。
// expected_net_bps=20（CROSS_VENUE 快照），realized=160USD → 160/80000×1e4=20bps → deviation=0 → spot_on。
// 旧 bug（不乘 CS）：notional=80 → 160/80×1e4=20000bps → deviation=19980 → better_than_expected（假象）。
func TestReviewNotionalMultipliesContractSize(t *testing.T) {
	opened := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	closed := opened.Add(30 * 24 * time.Hour)
	f := &fakeStore{
		positions: []pgstore.Position{
			mkPos(1, "CROSS_VENUE", 20, 160.0, opened, closed),
		},
		legs: map[int64][]pgstore.PositionLeg{
			1: {{Side: "BUY", Symbol: "UKOILm", Venue: "1314615:mt5", Qty: 1, PriceExpected: 80}},
		},
		cashflows: map[int64][]pgstore.Cashflow{},
		csByBrokerSymbol: map[[2]string]float64{
			{"1314615:mt5", "UKOILm"}: 1000,
		},
	}
	cfg := Config{MinSamples: 3, CrossBiasBps: 1.0, CarryBiasAnnualPct: 1.0}
	if err := Run(context.Background(), f, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.upserts) != 1 {
		t.Fatalf("期望 1 条，实际 %d", len(f.upserts))
	}
	e := f.upserts[0]
	if e.Verdict != "spot_on" {
		t.Errorf("verdict=%q（期望 spot_on，证明 CS 乘法生效），rationale=%q", e.Verdict, e.Rationale)
	}
	// 验证 rationale 中 notional=80000（非 80）
	if !strings.Contains(e.Rationale, "notional=80000.00") {
		t.Errorf("rationale 未含 notional=80000.00，实际=%q", e.Rationale)
	}
}
