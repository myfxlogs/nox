package main

import (
	"math"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// norm 便捷构造：经 normalize 走真归一链（classOf 测试默认空，按需传）。
func norm(venue, symbol string, hedgeSet map[string]bool, r Row) normRow {
	r.Symbol = symbol
	return normalize(venue, []Row{r}, hedgeSet, nil)[0]
}

func TestNormBase(t *testing.T) {
	for _, tc := range []struct{ sym, want string }{
		{"XAUUSDT", "XAU"}, {"XAU_USDT", "XAU"}, {"XAU-USDT-SWAP", "XAU"},
		{"BTCUSDT", "BTC"}, {"ETH_USDT", "ETH"},
	} {
		if got := normBase(tc.sym); got != tc.want {
			t.Fatalf("normBase(%q)=%q want %q", tc.sym, got, tc.want)
		}
	}
}

func TestCanonicalAlias(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"XAU", "XAUUSD"}, {"CL", "WTI"}, {"COPPER", "XCUUSD"}, {"BZ", "BRENT"},
		{"BTC", "BTC"}, // 未命中回退 base
	} {
		if got := canonicalOf(tc.base); got != tc.want {
			t.Fatalf("canonicalOf(%q)=%q want %q", tc.base, got, tc.want)
		}
	}
}

func TestTierOf(t *testing.T) {
	// 有腿=T1：canonical XAUUSD ∈ broker_symbol_map 腿集
	if n := norm("binance", "XAUUSDT", map[string]bool{"XAUUSD": true}, Row{Class: "COMMODITY", Status: "TRADING"}); n.Tier != "T1" || n.HedgeCanonical != "XAUUSD" {
		t.Fatalf("有腿 want T1+XAUUSD, got %s+%q", n.Tier, n.HedgeCanonical)
	}
	// symbol 本身 ∈ asset_equiv（PAXGUSDT 无别名但 asset_equiv 有 canonical=PAXGUSDT）：
	// HedgeCanonical 存实际命中成员 PAXGUSDT，不存 normBase 幻影值 PAXG
	if n := norm("binance", "PAXGUSDT", map[string]bool{"PAXGUSDT": true}, Row{Class: "COMMODITY", Status: "TRADING"}); n.Tier != "T1" || n.HedgeCanonical != "PAXGUSDT" {
		t.Fatalf("symbol 命中 asset_equiv want T1+PAXGUSDT, got %s+%q", n.Tier, n.HedgeCanonical)
	}
	// 无腿但别名命中=T2（非加密外观）
	if n := norm("binance", "XAUUSDT", nil, Row{Class: "", Status: "TRADING"}); n.Tier != "T2" {
		t.Fatalf("别名命中 want T2, got %s", n.Tier)
	}
	// 无腿 Class≠COIN=T2
	if n := norm("binance", "TSLAUSDT", nil, Row{Class: "EQUITY", Status: "TRADING"}); n.Tier != "T2" {
		t.Fatalf("Class=EQUITY want T2, got %s", n.Tier)
	}
	// 普通加密=T3
	if n := norm("binance", "BTCUSDT", nil, Row{Class: "COIN", Status: "TRADING"}); n.Tier != "T3" {
		t.Fatalf("BTCUSDT want T3, got %s", n.Tier)
	}
}

// TestAnnualize funding 年化口径：interval 逐符号真值，Gate 14400s→4h、OKX 4h → ×6 非 ×3。
func TestAnnualize(t *testing.T) {
	// rate 0.0001（1bps/期）× 6 期/日 × 365 = 21.9%/yr；若误按 8h×3=10.95%
	got := annualize(0.0001, 4)
	if got == nil || math.Abs(*got-21.9) > 0.01 {
		t.Fatalf("annualize(1bps,4h)=%v want ~21.9%%（×6 非 ×3=10.95%%）", got)
	}
	if math.Abs(*got-10.95) < 0.01 {
		t.Fatal("年化=×3 口径——interval 被当 8h 处理")
	}
	if annualize(0.0001, 0) != nil {
		t.Fatal("intervalH=0 want nil（无 funding 数据）")
	}
}

func TestDiffUniverse(t *testing.T) {
	old := map[string]pgstore.CexUniverseRow{
		"binance|OLDUSDT":  {Venue: "binance", Symbol: "OLDUSDT", Status: "TRADING", Tier: "T3", Class: "COIN"},
		"binance|RELUSDT":  {Venue: "binance", Symbol: "RELUSDT", Status: "SUSPENDED", Tier: "T3", Class: "COIN"},
		"binance|XAUUSDT":  {Venue: "binance", Symbol: "XAUUSDT", Status: "TRADING", Tier: "T2", Class: "COMMODITY"},
		"binance|KEEPUSDT": {Venue: "binance", Symbol: "KEEPUSDT", Status: "TRADING", Tier: "T3", Class: "COIN"},
	}
	hedge := map[string]bool{"XAUUSD": true}
	rows := normalize("binance", []Row{
		{Symbol: "OLDUSDT", Class: "COIN", Status: "BREAK"},        // → DELISTED
		{Symbol: "RELUSDT", Class: "COIN", Status: "TRADING"},      // → RELISTED
		{Symbol: "XAUUSDT", Class: "COMMODITY", Status: "TRADING"}, // → HEDGE_APPEARED（T2→T1）
		{Symbol: "KEEPUSDT", Class: "COIN", Status: "TRADING"},     // 无变化
		{Symbol: "NEWUSDT", Class: "COIN", Status: "TRADING"},      // → NEW
		{Symbol: "TSLAUSDT", Class: "EQUITY", Status: "TRADING"},   // → NEW + NEW_CLASS(EQUITY)
	}, hedge, nil)
	evs := diffUniverse(old, rows)
	got := make(map[string]string)
	for _, e := range evs {
		got[e.Symbol+":"+e.EventType] = e.Detail
	}
	for _, want := range []string{
		"OLDUSDT:DELISTED", "RELUSDT:RELISTED", "XAUUSDT:HEDGE_APPEARED",
		"NEWUSDT:NEW", "TSLAUSDT:NEW", "TSLAUSDT:NEW_CLASS",
	} {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing event %s; got %v", want, got)
		}
	}
	if _, ok := got["KEEPUSDT:NEW"]; ok {
		t.Fatal("KEEPUSDT 无变化不应出事件")
	}
	if len(evs) != 6 {
		t.Fatalf("want 6 events, got %d: %v", len(evs), got)
	}
}

// TestDiffUniverseBootstrap 旧 map 空=首轮 bootstrap：零事件零告警（防 ~2200 NEW 洪泛）。
func TestDiffUniverseBootstrap(t *testing.T) {
	rows := normalize("binance", []Row{
		{Symbol: "BTCUSDT", Class: "COIN", Status: "TRADING"},
		{Symbol: "XAUUSDT", Class: "COMMODITY", Status: "TRADING"},
	}, map[string]bool{"XAUUSD": true}, nil)
	if evs := diffUniverse(nil, rows); len(evs) != 0 {
		t.Fatalf("bootstrap want 0 events, got %d", len(evs))
	}
}

// TestFundingAlertRows funding 告警门槛：|ann|≥15%/yr 且 vol24h≥$5M。
func TestFundingAlertRows(t *testing.T) {
	rows := []normRow{
		{Row: Row{Symbol: "A", Vol24h: 6e6}, FundingAnn: fptr(16.0)},  // 过
		{Row: Row{Symbol: "B", Vol24h: 6e6}, FundingAnn: fptr(-16.0)}, // 过（负值绝对值）
		{Row: Row{Symbol: "C", Vol24h: 1e6}, FundingAnn: fptr(30.0)},  // 量不足
		{Row: Row{Symbol: "D", Vol24h: 6e6}, FundingAnn: fptr(10.0)},  // ann 不足
		{Row: Row{Symbol: "E", Vol24h: 6e6}},                          // 无 funding 数据
	}
	out := fundingAlertRows(rows, 15, 5e6)
	if len(out) != 2 || out[0].Symbol != "A" || out[1].Symbol != "B" {
		t.Fatalf("want [A B], got %v", out)
	}
}

// TestDivergences 同 canonical 跨所分化 ≥20%/yr 出事件、<20% 不出、同所不比。
func TestDivergences(t *testing.T) {
	mk := func(venue, symbol string, ann, vol float64) normRow {
		return normRow{Row: Row{Symbol: symbol, Vol24h: vol}, Venue: venue,
			Canonical: canonicalOf(normBase(symbol)), FundingAnn: fptr(ann)}
	}
	rows := []normRow{
		mk("binance", "XAUUSDT", 25.0, 6e6),    // XAUUSD
		mk("okx", "XAU-USDT-SWAP", 2.0, 6e6),   // XAUUSD；|25−2|=23≥20 → 事件
		mk("gate", "XAU_USDT", 26.0, 6e6),      // XAUUSD；与 binance 差 1、与 okx 差 24 → 事件
		mk("binance", "BTCUSDT", 5.0, 1e8),     // BTC；与 okx 差 3 <20
		mk("okx", "BTC-USDT-SWAP", 8.0, 1e8),   // BTC
		mk("binance", "ETHUSDT", 30.0, 1e6),    // ETH；vol 不足不参与
		mk("okx", "ETH-USDT-SWAP", -30.0, 1e6), // ETH
	}
	evs := divergences(rows, 20, 5e6)
	// XAUUSD：3 所两两 → binance:okx 23、gate:okx 24 ≥20 → 2 事件（binance:gate 差 1 不出）
	if len(evs) != 2 {
		t.Fatalf("want 2 divergence events, got %d: %v", len(evs), evs)
	}
	for _, e := range evs {
		if e.Venue != "multi" || e.Symbol != "XAUUSD" || e.EventType != "FUNDING_DIVERGENCE" {
			t.Fatalf("bad divergence row: %+v", e)
		}
	}
	// BTC 3<20、ETH vol 不足 → 均无事件
	for _, e := range evs {
		if e.Symbol == "BTC" || e.Symbol == "ETH" {
			t.Fatalf("unexpected divergence: %+v", e)
		}
	}
}

// TestCooldownOK last_alert_ts 24h 冷却判定。
func TestCooldownOK(t *testing.T) {
	now := time.Now()
	if !cooldownOK(nil, now) {
		t.Fatal("nil last_alert_ts want OK")
	}
	if cooldownOK(ptrTime(now.Add(-time.Hour)), now) {
		t.Fatal("1h 内 want 冷却中")
	}
	if !cooldownOK(ptrTime(now.Add(-25*time.Hour)), now) {
		t.Fatal("25h want 可重发")
	}
}

func fptr(v float64) *float64        { return &v }
func ptrTime(t time.Time) *time.Time { return &t }
