// d350_spot_carry_test.go —— D-350 T4：digest 两新段渲染（候选前三行截断/纸面活跃行/空段不渲染）。
package main

import (
	"strings"
	"testing"
)

func TestD350_RenderSpotCarrySection(t *testing.T) {
	if got := RenderSpotCarrySection(nil); got != "" {
		t.Errorf("空行集应不渲染，got %q", got)
	}
	rows := []SpotCarryUniverseRow{
		{Symbol: "B2USDT", Venue: "binance", FundingAnn: 120.4, NetAnn: 120.25},
		{Symbol: "GUSDT", Venue: "binance", FundingAnn: 91.9, NetAnn: 91.75},
		{Symbol: "XUSDT", Venue: "binance", FundingAnn: 40, NetAnn: 39.85},
		{Symbol: "YUSDT", Venue: "binance", FundingAnn: 30, NetAnn: 29.85}, // 第四行截断
	}
	got := RenderSpotCarrySection(rows)
	// D-353 S2：标题诚实标注带内口径（band 20-300，与纸面闸同值对齐）
	for _, want := range []string{"spot-carry 候选（band 20-300 %/yr，独立排名）", "B2USDT", "120.25", "GUSDT", "91.75"} {
		if !strings.Contains(got, want) {
			t.Errorf("段缺 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "YUSDT") {
		t.Error("第四行应截断（前三行标注）")
	}
}

func TestD350_RenderPaperSpotCarrySection(t *testing.T) {
	if got := RenderPaperSpotCarrySection(nil); got != "" {
		t.Errorf("空行集应不渲染，got %q", got)
	}
	got := RenderPaperSpotCarrySection([]PaperSpotCarryRow{{
		ID: 1, Symbol: "GUSDT", Side: "short_perp", QtyUSD: 1000,
		EntryFundingAnn: 92, LastFundingAnn: 88, AccruedUSD: 2.5205, EntryTs: "09-19 12:00",
	}})
	for _, want := range []string{"spot-carry 纸面", "GUSDT", "short_perp", "2.5205", "09-19 12:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("段缺 %q:\n%s", want, got)
		}
	}
}
