// d369_watchlist_e2e_test.go —— D-369 S2/S4/S5：watchlist provider/seed/E2E 锚（DSN 级）。
package main

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/testutil"
	"nox/internal/research/detector"
	"nox/internal/store/pgstore"
)

// d369FactProvider detector.FactProvider 本地 fake（E2E ② 段 detector 产候选用）。
type d369FactProvider struct {
	funding map[string]map[string]float64
	isReal  map[string]*bool
}

func (f d369FactProvider) FundingFacts() map[string]map[string]float64 { return f.funding }
func (f d369FactProvider) SwapFacts() map[string]map[string][2]float64 { return nil }
func (f d369FactProvider) TradeModes() map[string]map[string]int64     { return nil }
func (f d369FactProvider) IsRealMap() map[string]*bool                 { return f.isReal }

func d369Store(t *testing.T) *pgstore.Store {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	s, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestSymbolSetProvider_WatchlistMerge D-369 S2：provider.Symbols=watchlist 全符号；
// refresh 刷新（新行并入）。
func TestSymbolSetProvider_WatchlistMerge(t *testing.T) {
	s := d369Store(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE radar_watchlist`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.UpsertRadarWatchlist(ctx, "ONDO", []string{"binance", "gate"}, "divergence"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	w := newWatchlistSymbolProvider(ctx, s, nil)
	syms := w.Symbols("binance")
	if len(syms) != 1 || syms[0] != "ONDO" {
		t.Fatalf("Symbols = %v, want [ONDO]", syms)
	}
	// 新行 → refresh 后并入
	if err := s.UpsertRadarWatchlist(ctx, "MYX", []string{"gate"}, "divergence"); err != nil {
		t.Fatalf("upsert MYX: %v", err)
	}
	w.refresh(ctx)
	if got := w.Symbols("binance"); len(got) != 1 || got[0] != "ONDO" {
		t.Errorf("D-532 venue 过滤：MYX{gate} 不进 binance 集，got %v want [ONDO]", got)
	}
	if got := w.Symbols("gate"); len(got) != 2 {
		t.Errorf("gate 集应 [MYX ONDO], got %v", got)
	}
}

// TestD369_E2E_ONDO S5 端到端（R1 改义）：fake watchlist 行 ONDO{binance:perp,gate:perp} →
// provider 合并 → detector 产候选（S3 通道）→ venue 白名单可放行为止（seed 链路已裁撤，
// combo 行不在断言面；不验真下单，executor 轨不变）。
func TestD369_E2E_ONDO(t *testing.T) {
	s := d369Store(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE radar_watchlist, xasset_allow_combos`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	f := false // usdt 标 realness 指针
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret, simulated)
		VALUES ('binance', 'perp', 'k', 's', true), ('gate', 'perp', 'k', 's', true)
		ON CONFLICT (venue, venue_type, simulated) DO NOTHING`); err != nil {
		t.Fatalf("seed exchange_configs: %v", err)
	}
	if err := s.UpsertRadarWatchlist(ctx, "ONDO", []string{"binance", "gate"}, "divergence"); err != nil {
		t.Fatalf("seed watchlist: %v", err)
	}

	// ① provider 合并（watchlist ∪ env；本测试 env 空 → watchlist 单源）
	w := newWatchlistSymbolProvider(ctx, s, nil)
	syms := w.Symbols("binance")
	found := false
	for _, s2 := range syms {
		if s2 == "ONDO" {
			found = true
		}
	}
	if !found {
		t.Fatalf("provider.Symbols 应含 ONDO, got %v", syms)
	}

	// ② detector 产候选（S3 sim-pair 通道）
	now := time.Now()
	snapshot := map[string]map[string]bus.Quote{
		"ONDOUSDT": {
			"binance:perp": {Symbol: "ONDOUSDT", Venue: "binance", VenueType: "perp", Bid: 1.0, Ask: 1.001, Time: now},
			"gate:perp":    {Symbol: "ONDOUSDT", Venue: "gate", VenueType: "perp", Bid: 0.99, Ask: 1.0, Time: now},
		},
	}
	funding := map[string]map[string]float64{"ONDOUSDT": {"binance": 11.0, "gate": -16.2}}
	isReal := map[string]*bool{"binance:perp": &f, "gate:perp": &f} // D-371 S3：venueKey 粒度
	fp := d369FactProvider{funding: funding, isReal: isReal}
	d := &detector.PerpFundingDetector{MinNetAnnualized: 2.0, FreshnessTTL: 30 * time.Second}
	cands := d.Scan(ctx, snapshot, fp)
	if len(cands) != 1 || cands[0].Canonical != "ONDOUSDT" {
		t.Fatalf("detector 候选 = %+v, want 1 条 ONDOUSDT（S3 通道）", cands)
	}

	// ③ venue 白名单可放行断言（D-371 后 ListVenueIsReal 键=venue:type；双腿已知 sim）
	isRealMap, err := s.ListVenueIsReal(ctx)
	if err != nil {
		t.Fatalf("venue is_real: %v", err)
	}
	for _, vk := range []string{"binance:perp", "gate:perp"} {
		isReal := isRealMap[vk]
		if isReal == nil || *isReal {
			t.Errorf("venue %s 应已知 sim（可放行）, got %v", vk, isReal)
		}
	}
}
