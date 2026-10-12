// cex_venue_key_test.go —— D-362 S6：dashboard 侧 winners 计算锚（monitor/持仓查询器/
// executor 三处同源调用的同一实现；纯函数无 DB）。
package dashboard

import (
	"testing"
	"time"

	"nox/internal/ledger/reconcile"
	"nox/internal/store/pgstore"
)

// TestCexKeyWinners_D362 created_at 最大者赢（并列取 id 大）；与 reconcile.CexVenueKey
// 组合复现 binance:perp 实证（id20 real 早建 + id25 sim 后建 → sim 恒赢保 canonical）。
func TestCexKeyWinners_D362(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cfgs := []pgstore.ExchangeConfig{
		{ID: 20, Venue: "binance", VenueType: "perp", CreatedAt: base},                // real 先建
		{ID: 25, Venue: "binance", VenueType: "perp", CreatedAt: base.Add(time.Hour)}, // sim 后建
		{ID: 24, Venue: "gate", VenueType: "perp", CreatedAt: base},                   // 单 config 无撞键
	}
	winners := cexKeyWinners(cfgs)
	if winners["binance:perp"] != 25 {
		t.Errorf("winner binance:perp = %d, want 25（后写覆盖赢家语义显式化）", winners["binance:perp"])
	}
	if winners["gate:perp"] != 24 {
		t.Errorf("winner gate:perp = %d, want 24", winners["gate:perp"])
	}
	// 三处同源：real id20 → 后缀键；sim id25 → canonical
	if got := reconcile.CexVenueKey("binance", "perp", 20, winners); got != "binance:perp#20" {
		t.Errorf("real key = %q, want binance:perp#20", got)
	}
	if got := reconcile.CexVenueKey("binance", "perp", 25, winners); got != "binance:perp" {
		t.Errorf("sim key = %q, want binance:perp（canonical 不变）", got)
	}
	// created_at 并列 → id 大者赢（确定性）
	tie := []pgstore.ExchangeConfig{
		{ID: 30, Venue: "okx", VenueType: "spot", CreatedAt: base},
		{ID: 31, Venue: "okx", VenueType: "spot", CreatedAt: base},
	}
	if w := cexKeyWinners(tie); w["okx:spot"] != 31 {
		t.Errorf("tie winner okx:spot = %d, want 31（id 大者）", w["okx:spot"])
	}
}
