package main

import (
	"testing"

	v1 "nox/internal/gen/dashboardv1"
)

// TestLiveEquityMap_OkxUnifiedKeyNorm okx unified/funding 按币种映射缺锚：
// 账户行原始 VenueType=perp（配置行域）→归一 unified 键与快照池对上；
// USDT 归现金池键，非 USDT（DOGE）归同池币种估值键；funding 键穿透不动。
func TestLiveEquityMap_OkxUnifiedKeyNorm(t *testing.T) {
	m := liveEquityMapFromAccounts([]*v1.AccountInfo{
		{Venue: "okx", VenueType: "perp", AssetType: "cex", Holdings: []*v1.Holding{
			{Ccy: "USDT", Qty: 120}, {Ccy: "DOGE", Qty: 300},
		}},
		{Venue: "okx", VenueType: "funding", AssetType: "cex", Currency: "USDT", Equity: 55},
	})
	if m["okx:unified:USDT"] != 120 || m["okx:unified:DOGE"] != 300 {
		t.Fatalf("unified 归一键缺/值错: %+v", m)
	}
	if m["okx:funding:USDT"] != 55 {
		t.Fatalf("funding 键应穿透不动: %+v", m)
	}
	if _, bad := m["okx:perp:USDT"]; bad {
		t.Fatalf("原始型键不应残留: %+v", m)
	}
}
