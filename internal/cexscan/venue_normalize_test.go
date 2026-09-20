// venue_normalize_test.go —— D-361 S4 锚：统一账户写侧归一+同键去重（纯函数）。
package cexscan

import (
	"testing"
	"time"
)

// TestNormalizeVenueType 归一锚：okx cex → unified；binance/gate 现货合约分钱包不归一；
// 非 cex 不归一。
func TestNormalizeVenueType(t *testing.T) {
	cases := []struct {
		name                    string
		assetType, venue, vType string
		want                    string
	}{
		{"okx 统一账户归一", "cex", "okx", "spot", "unified"},
		{"okx perp 同归 unified", "cex", "okx", "perp", "unified"},
		{"binance 分钱包不归一", "cex", "binance", "spot", "spot"},
		{"gate 分钱包不归一", "cex", "gate", "perp", "perp"},
		{"非 cex 不归一", "cfd", "okx", "mt5", "mt5"},
	}
	for _, c := range cases {
		if got := normalizeVenueType(c.assetType, c.venue, c.vType); got != c.want {
			t.Errorf("%s: normalizeVenueType(%q,%q,%q) = %q, want %q", c.name, c.assetType, c.venue, c.vType, got, c.want)
		}
	}
}

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
