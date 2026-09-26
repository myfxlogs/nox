package dashboard

// D-327 T1 锚（manual 端到端）：XASSET_CARRY 开仓 EXECUTED 后经 meta 写点的 S1 补写 →
// expected_entry_spread_bps = (buy−sell)/buy×1e4 非 NULL。删 S1 写回（变异）→ 本锚红。

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// d327FakeExecutor 双腿假执行器：PlaceOrder 恒按预设价 FILLED（LegResult.Price→legs.price_actual）。
type d327FakeExecutor struct {
	venue string
	price float64
}

func (e *d327FakeExecutor) Key() string { return e.venue }
func (e *d327FakeExecutor) PlaceOrder(_ context.Context, o exec.Order) (exec.Fill, error) {
	return exec.Fill{OrderID: "d327-" + e.venue, Price: e.price, Qty: o.Qty, Ts: time.Now()}, nil
}
func (e *d327FakeExecutor) CloseOrder(_ context.Context, _ exec.OrderRef) (exec.Fill, error) {
	return exec.Fill{OrderID: "d327-close-" + e.venue, Price: e.price, Qty: 1, Ts: time.Now()}, nil
}

// d327CSResolver pipeline 级 XASSET 组合门的 CS 查询（D-311 S3 fail-closed 需接线）。
type d327CSResolver struct{}

func (d327CSResolver) ContractSize(string, string) (float64, bool) { return 1.0, true }

// TestD327_ExecuteInternal_EntrySpreadBackfilled T1：开仓 EXECUTED 后 entry spread 基准落列。
// D-378a S1 改义：XASSET 锚=纯 quote 口径（fill 口径吸收 venue 成交噪声=493 毒锚根因）——
// buy quote bid 60.00 / ask 60.02、sell quote ask 60.02 → (60.00−60.02)/60.02×1e4 ≈ −3.332。
// sellCanonical 走 broker_symbol_map 反查（opp.CanonicalSell NULL）。
func TestD327_ExecuteInternal_EntrySpreadBackfilled(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE autotrade_state, facts, opportunities, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("seed RUNNING: %v", err)
	}
	// D-378a S1：quote 锚需卖腿 canonical 反查（3001157 venue 裸名 × 卖腿 symbol）
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ('3001157','mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING; INSERT INTO broker_symbol_map (venue, broker_symbol, canonical) VALUES ('3001157','USOILm','CLUSDT') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed symbol map: %v", err)
	}
	oppID := "XASSET_CARRY:CLUSDT:gate:perp:3001157:mt5"
	if _, err := st.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "CLUSDT",
		VenueBuy: "gate:perp", VenueSell: "3001157:mt5", NetBps: 50,
		ExpiresAt: time.Now().Add(time.Hour), QuoteTime: time.Now(),
	}); err != nil {
		t.Fatalf("seed opp: %v", err)
	}
	// D-328 regime 闸放行样本：卖腿 gate:perp 收正 funding（13 条 ≥+5）
	base := time.Now().Add(-65 * time.Minute)
	for i := 0; i < 13; i++ {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
			 VALUES ('funding', 'gate', 'CLUSDT', $1, 'pct_annualized', $2, 'd327test')`,
			5.0+float64(i), base.Add(time.Duration(i)*5*time.Minute)); err != nil {
			t.Fatalf("seed funding %d: %v", i, err)
		}
	}

	qb := bus.New()
	now := time.Now()
	qb.Publish(bus.Quote{Symbol: "CLUSDT", Venue: "gate", VenueType: "perp", Bid: 60.00, Ask: 60.02, Time: now, VenueSymbol: "CL_USDT"})
	qb.Publish(bus.Quote{Symbol: "CLUSDT", Venue: "3001157", VenueType: "mt5", Bid: 60.00, Ask: 60.02, Time: now, VenueSymbol: "USOILm"})

	reg := exec.NewRegistry()
	reg.Register(&d327FakeExecutor{venue: "gate:perp", price: 60.02})   // BUY 腿成交=ask
	reg.Register(&d327FakeExecutor{venue: "3001157:mt5", price: 60.00}) // SELL 腿成交=bid
	pipeline := exec.NewPipeline(reg, st, qb, exec.RiskLimits{}, "demo")
	pipeline.SetXAssetComboAllow([]exec.XAssetComboAllowSpec{
		{CanonicalBuy: "CLUSDT", CanonicalSell: "CLUSDT", VenueBuy: "gate:perp", VenueSell: "3001157:mt5", MaxLongQty: 10, MaxShortQty: 10, MaxGrossUSD: 50000},
	})
	pipeline.SetContractSizeResolver(d327CSResolver{}) // pipeline 级 XASSET 门 CS fail-closed 需接线

	svc := &Service{
		store: st, qbus: qb,
		pipeline:  pipeline,
		liveTrade: true,
	}
	svc.SetLegContractSize(func(venueKey, venueSymbol string) (float64, bool) { return 1.0, true })
	svc.SetCexContractSize(func(venue, venueSymbol string) (float64, bool) { return 1.0, true })
	svc.SetXAssetComboAllow([]exec.XAssetComboAllowSpec{
		{CanonicalBuy: "CLUSDT", CanonicalSell: "CLUSDT", VenueBuy: "gate:perp", VenueSell: "3001157:mt5", MaxLongQty: 10, MaxShortQty: 10, MaxGrossUSD: 50000},
	})
	svc.SetCarryRegime(exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12})

	// D-495：Execute 预检环腿可平性闸——双腿播亮 ticks（gate CL_USDT→CLUSDT 归一键 /
	// 3001157 USOILm 原生名）。
	if err := st.EnsureMonthPartition(ctx, now.Year(), int(now.Month())); err != nil {
		t.Fatalf("partition: %v", err)
	}
	if err := st.InsertTicks(ctx, []pgstore.TickRow{
		{Ts: now.Add(-2 * time.Minute), Venue: "gate", Symbol: "CLUSDT", Bid: 60.00, Ask: 60.02, VenueType: "cex"},
		{Ts: now, Venue: "gate", Symbol: "CLUSDT", Bid: 60.05, Ask: 60.07, VenueType: "cex"},
		{Ts: now.Add(-2 * time.Minute), Venue: "3001157", Symbol: "USOILm", Bid: 60.00, Ask: 60.02, VenueType: "mt5"},
		{Ts: now, Venue: "3001157", Symbol: "USOILm", Bid: 60.04, Ask: 60.06, VenueType: "mt5"},
	}); err != nil {
		t.Fatalf("seed ticks: %v", err)
	}

	status, msg, posID := svc.ExecuteOpportunityInternal(ctx, oppID, "d327-t1", 1.0)
	// 成功态=pipeline 透传 "OPEN"（result.Status，非字面 EXECUTED）
	if posID == 0 || status == "FAILED" {
		t.Fatalf("开仓应成功, got %s/%d msg=%s", status, posID, msg)
	}
	var got *float64
	if err := st.Pool().QueryRow(ctx,
		`SELECT expected_entry_spread_bps FROM positions WHERE id = $1`, posID).Scan(&got); err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	if got == nil {
		t.Fatal("EXECUTED 后 XASSET_CARRY 基准应非 NULL（S1 写点）")
	}
	want := (60.00 - 60.02) / 60.02 * 10000
	if math.Abs(*got-want) > 1e-9 {
		t.Errorf("baseline = %.6f, want %.6f（D-378a quote 口径）", *got, want)
	}
}
