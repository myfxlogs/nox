package scan

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// TestCfdBookGate_DispatchLine D-622 三批：簿缺席/锚死 warn 末行实施席转发令。
func TestCfdBookGate_DispatchLine(t *testing.T) {
	fs := &fakeMtmPaperStore{rows: map[int64]*pgstore.PaperSpotCarry{}, ticks: map[string][]pgstore.TickRow{}}
	active := []pgstore.PaperSpotCarry{{
		Structure: "short_cfd_long_perp", VenueA: "3001157", VenueB: "binance",
		Symbol: "XAUUSD", EntryTs: time.Now().Add(-time.Hour), Status: "active",
	}}
	view := newAssetClassView(nil)
	warnActiveBookAbsent(context.Background(), fs, active, view, time.Now())
	if len(fs.alerts) == 0 {
		t.Fatal("簿缺席 warn 未发")
	}
	if !strings.HasSuffix(fs.alerts[0], "[角色:实施] cfd纸面仓簿排查（读 STATE 本行——entry_basis 锚死径）") {
		t.Fatalf("末行令缺: %q", fs.alerts[0])
	}
}
