// d430_margin_test.go —— D-430：margin-guard tick 键域修复（legTickKey 分域解析 + T1-T5 锚）。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// TestD430_Evaluate_MT_Unmapped T4：MT 腿符号未入 broker_symbol_map → data_stale
// （fail-closed 语义保持，notional 链正常——data_stale 来源=键解析缺）。
func TestD430_Evaluate_MT_Unmapped(t *testing.T) {
	s := d405Store(t)
	ctx := context.Background()
	venue, vt := "8888010", "mt5"
	// 腿在、notional 可解（XAUUSD USD 计价），但 FOOm 无映射 → data_stale
	d405SeedMTLegNoMap(t, s, venue+":"+vt, "FOOm", "XASSET_CARRY:XAUUSD:a:b", 1, 2000)
	qb := bus.New() // D-565 S6：腿级实时报价（存价回退已删）
	qb.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "okx", VenueType: "perp", Bid: 2000, Ask: 2000.1})
	g := NewMarginGuard(s, qb, d405CSStub, time.Minute, time.Minute)
	sn := &pgstore.VenueAccountSnapshot{
		Venue: venue, VenueType: vt, EquityRaw: 5000, FreeMarginRaw: 9999,
		Currency: "USD", FetchedAt: time.Now(),
	}
	if err := g.evaluate(ctx, sn); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	r, ok, err := s.GetVenueMarginStatus(ctx, venue, vt)
	if err != nil || !ok {
		t.Fatalf("GetVenueMarginStatus: ok=%v err=%v", ok, err)
	}
	if r.Tier != "data_stale" {
		t.Fatalf("tier=%s want data_stale（未映射 fail-closed）", r.Tier)
	}
}

func d405SeedMTLegNoMap(t *testing.T, s *pgstore.Store, venueKey, symbol, oppID string, qty, price float64) int64 {
	t.Helper()
	ctx := context.Background()
	posID, err := s.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: oppID, Type: "XASSET_CARRY", Status: "OPEN", Environment: "live",
	})
	if err != nil {
		t.Fatalf("create pos: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 1, Phase: "OPEN", Venue: venueKey, Symbol: symbol,
		Side: "BUY", Qty: qty, Status: "FILLED", FilledQty: &qty, AvgFillPrice: &price,
	}); err != nil {
		t.Fatalf("create leg: %v", err)
	}
	return posID
}

// TestD430_Evaluate_CEX_Perpetual T5 端到端（先红后绿锚）：腿 venue_symbol=XAU-USDT-SWAP
// + ticks 存 (okx,perp,XAUUSDT) → tier 非 data_stale 且 DailyVolUSD>0。
// 红：现码查 (okx,unified,XAU-USDT-SWAP) 必 0 行 → data_stale。
func TestD430_Evaluate_CEX_Perpetual(t *testing.T) {
	s := d405Store(t)
	ctx := context.Background()
	// ticks：venue=okx, venue_type=perp, symbol=XAUUSDT（tick 域真实键）
	now := time.Now()
	if err := s.EnsureMonthPartition(ctx, now.Year(), int(now.Month())); err != nil {
		t.Fatalf("EnsureMonthPartition: %v", err)
	}
	if err := s.InsertTicks(ctx, []pgstore.TickRow{
		{Ts: now.Add(-3 * time.Hour), Venue: "okx", VenueType: "perp", Symbol: "XAUUSDT", Bid: 100, Ask: 100.1},
		{Ts: now.Add(-30 * time.Minute), Venue: "okx", VenueType: "perp", Symbol: "XAUUSDT", Bid: 110, Ask: 110.1},
	}); err != nil {
		t.Fatalf("seed ticks: %v", err)
	}
	// 腿：venue_key=okx:perp, venue_symbol=XAU-USDT-SWAP（快照域过滤仍走 legSnapshotVenue→unified）
	posID, err := s.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: "XASSET_CARRY:XAUUSD:okx:perp:3001157:mt5", Type: "XASSET_CARRY",
		Status: "OPEN", Environment: "live",
	})
	if err != nil {
		t.Fatalf("create pos: %v", err)
	}
	qty, price := 1.0, 2000.0
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 1, Phase: "OPEN", Venue: "okx:perp", Symbol: "XAU-USDT-SWAP",
		Side: "SELL", Qty: qty, Status: "FILLED", FilledQty: &qty, AvgFillPrice: &price,
	}); err != nil {
		t.Fatalf("create leg: %v", err)
	}
	qb := bus.New() // D-565 S6：腿级实时报价（存价回退已删）
	qb.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "okx", VenueType: "perp", Bid: 2000, Ask: 2000.1})
	g := NewMarginGuard(s, qb, d405CSStub, time.Minute, time.Minute)
	sn := &pgstore.VenueAccountSnapshot{
		Venue: "okx", VenueType: "unified", EquityRaw: 10000, BufferMultiple: f64p(5),
		Currency: "USD", FetchedAt: time.Now(),
	}
	if err := g.evaluate(ctx, sn); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	r, ok, err := s.GetVenueMarginStatus(ctx, "okx", "unified")
	if err != nil || !ok {
		t.Fatalf("GetVenueMarginStatus: ok=%v err=%v", ok, err)
	}
	if r.Tier == "data_stale" {
		t.Fatal("tier=data_stale（红=tick 键域错位未修）")
	}
	if r.DailyVolUSD == nil || *r.DailyVolUSD <= 0 {
		t.Fatalf("DailyVolUSD=%v want >0", r.DailyVolUSD)
	}
}
