// venue_normalize_test.go —— D-361 S4/D-363-R1 锚：写侧归一+同键去重集成（buildVenueSnapshotRows；纯函数 NormalizeVenueType 已下沉 internal/venue 见 account_real_test.go）。
package cexscan

import (
	"testing"
	"time"
)

// TestBuildVenueSnapshotRows_OKXDedup 集成锚：okx spot+perp 双 snap（双 config 同钱）→
// 落库仅一行 venue_type=unified；real 过滤（D-360）与 MT 行语义不回归。
func TestBuildVenueSnapshotRows_OKXDedup(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	snaps := []AccountSnapshot{
		{Venue: "okx", VenueType: "spot", AssetType: "cex", Equity: 52.40, Currency: "USDT", Ts: now},
		{Venue: "okx", VenueType: "perp", AssetType: "cex", Equity: 52.40, Currency: "USDT", Ts: now},
		{Venue: "3001157", VenueType: "mt5", AssetType: "cfd", Equity: 1000, Currency: "USD", Ts: now},
		{Venue: "52993526", VenueType: "mt5", AssetType: "cfd", Equity: 8000, Currency: "USD", Ts: now},
		{Venue: "binance", VenueType: "perp", AssetType: "cex", Simulated: true, Equity: 4533, Currency: "USDT", Ts: now},
		{Venue: "binance", VenueType: "spot", AssetType: "cex", Equity: 0.40, Currency: "USDT", Ts: now},
	}
	rows := buildVenueSnapshotRows(snaps, map[string]bool{"3001157": true})
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3（okx 去重 1+MT real 1+binance spot 1）: %+v", len(rows), rows)
	}
	if rows[0].Venue != "okx" || rows[0].VenueType != "unified" {
		t.Errorf("rows[0] = %s/%s, want okx/unified（双 config 同钱仅一行）", rows[0].Venue, rows[0].VenueType)
	}
	if rows[0].EquityRaw != 52.40 {
		t.Errorf("okx unified equity = %v, want 52.40（首行保留不猜）", rows[0].EquityRaw)
	}
	for _, r := range rows {
		if r.Venue == "52993526" {
			t.Error("MT demo 应被 real 过滤（D-360 语义不回归）")
		}
		if r.Venue == "binance" && r.VenueType == "perp" {
			t.Error("binance perp sim 应被 real 过滤")
		}
	}
}

// TestBuildVenueSnapshotRows_OKXSubaccounts D-363-R1 S2：okx 四快照（spot+perp+funding+
// earn）→ 落三行（unified=交易账户双 config 去重；funding/earn 子账户各归各值原样穿透）。
// 旧实现（归一无条件压全部 type）红=funding/earn 塌进 unified 被去重（生产 23:06 实证
// earn $5,261 顶掉 unified $6.24）。
func TestBuildVenueSnapshotRows_OKXSubaccounts(t *testing.T) {
	now := time.Date(2026, 9, 21, 23, 6, 0, 0, time.UTC)
	snaps := []AccountSnapshot{
		{Venue: "okx", VenueType: "spot", AssetType: "cex", Equity: 6.24, Currency: "USDT", Ts: now},
		{Venue: "okx", VenueType: "perp", AssetType: "cex", Equity: 6.24, Currency: "USDT", Ts: now},
		{Venue: "okx", VenueType: "funding", AssetType: "cex", Equity: 0, Currency: "USDT", Ts: now},
		{Venue: "okx", VenueType: "earn", AssetType: "cex", Equity: 5261.25, Currency: "USDT", Ts: now},
	}
	rows := buildVenueSnapshotRows(snaps, map[string]bool{}) // cex real 无需 MT 键
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3（unified+funding+earn 各归各值）: %+v", len(rows), rows)
	}
	byType := map[string]float64{}
	for _, r := range rows {
		byType[r.VenueType] = r.EquityRaw
	}
	if byType["unified"] != 6.24 {
		t.Errorf("unified equity = %v, want 6.24（交易账户双 config 去重）", byType["unified"])
	}
	if byType["funding"] != 0 {
		t.Errorf("funding equity = %v, want 0（原样穿透）", byType["funding"])
	}
	if byType["earn"] != 5261.25 {
		t.Errorf("earn equity = %v, want 5261.25（原样穿透，不被 unified 顶掉）", byType["earn"])
	}
}
