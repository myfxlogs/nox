package dashboard

import (
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// TestD516_RealOnlyConfigs fail-closed 闸：simulated=t 行剔除且 warn-once 不重复刷日志；
// sim 行不占撞键 canonical（赢家只在 real 集内算）。
func TestD516_RealOnlyConfigs(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	cfgs := []pgstore.ExchangeConfig{
		{ID: 10, Venue: "binance", VenueType: "perp", CreatedAt: t0},
		{ID: 11, Venue: "okx", VenueType: "perp", Simulated: true, CreatedAt: t0.Add(time.Hour)},
		{ID: 12, Venue: "binance", VenueType: "perp", Simulated: true, CreatedAt: t0.Add(2 * time.Hour)},
	}
	var warned map[int64]bool
	live := realOnlyConfigs(cfgs, &warned, "test")
	if len(live) != 1 || live[0].ID != 10 {
		t.Fatalf("live = %+v, want 仅 id10 real 行", live)
	}
	if len(warned) != 2 || !warned[11] || !warned[12] {
		t.Fatalf("warned = %v, want id11+id12 各一记", warned)
	}
	// warn-once：第二轮同 id 不重复计数（map 不变=不再打日志）。
	live2 := realOnlyConfigs(cfgs, &warned, "test")
	if len(live2) != 1 || len(warned) != 2 {
		t.Fatalf("二轮 live=%d warned=%v, want 不变", len(live2), warned)
	}
	// sim 行不参与撞键：id12 虽最新但被剔除，canonical 归 real id10。
	w := cexKeyWinners(live)
	if got := w["binance:perp"]; got != 10 {
		t.Fatalf("winners[binance:perp] = %d, want 10（sim id12 不抢 canonical）", got)
	}
}
