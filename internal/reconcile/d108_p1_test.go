// d108_p1_test.go —— D-108 P1 A3：僵尸循环死代码修复单测（先红后绿）。
// autoFailOPENINGZombie：venue 命中（腿在 venue 真有持仓）→ 不置 FAILED + warn 告警；
// 未命中才置 FAILED。
package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// TestD108_AutoFailZombie_VenueHit 命中 venue → 不置 FAILED + warn 告警（交人工/对账）。
func TestD108_AutoFailZombie_VenueHit(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 200, Status: "OPENING", OpportunityID: "SWAPARB:AUDJPY:vA:vB:1"}
	fs.positions[200] = pos
	// 腿有 ticket，且 venueOrders 含该 ticket = venue 命中
	fs.legs[200] = []*pgstore.PositionLeg{
		{PositionID: 200, LegNo: 0, Phase: "OPEN", Venue: "vA:mt5", OrderID: "T100", Side: "BUY", Qty: 1.0, Status: "FILLED"},
	}
	venueOrders := map[string]VenueOrder{"T100": {Ticket: "T100", Symbol: "AUDJPY", Side: "BUY", Volume: 1.0}}

	r := &Reconciler{store: fs, runTimeout: 5 * time.Second}
	if err := r.autoFailOPENINGZombie(context.Background(), pos, venueOrders); err != nil {
		t.Fatalf("autoFailOPENINGZombie: %v", err)
	}
	// 不应置 FAILED
	for _, id := range fs.failedIDs {
		if id == 200 {
			t.Fatal("venue hit: position should NOT be FAILED, but was")
		}
	}
	// 应有 warn 告警
	found := false
	for _, a := range fs.alerts {
		if a.level == "warn" && strings.Contains(a.msg, "不归档 FAILED") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("venue hit: expected warn alert '不归档 FAILED', got: %v", fs.alerts)
	}
}

// TestD108_AutoFailZombie_VenueMiss 未命中 venue → 置 FAILED + warn 告警。
func TestD108_AutoFailZombie_VenueMiss(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 201, Status: "OPENING", OpportunityID: "SWAPARB:AUDJPY:vA:vB:1"}
	fs.positions[201] = pos
	// 腿有 ticket，但 venueOrders 不含该 ticket = 未命中
	fs.legs[201] = []*pgstore.PositionLeg{
		{PositionID: 201, LegNo: 0, Phase: "OPEN", Venue: "vA:mt5", OrderID: "T200", Side: "BUY", Qty: 1.0, Status: "FILLED"},
	}
	venueOrders := map[string]VenueOrder{} // 空 = 未命中

	r := &Reconciler{store: fs, runTimeout: 5 * time.Second}
	if err := r.autoFailOPENINGZombie(context.Background(), pos, venueOrders); err != nil {
		t.Fatalf("autoFailOPENINGZombie: %v", err)
	}
	// 应置 FAILED
	found := false
	for _, id := range fs.failedIDs {
		if id == 201 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("venue miss: position should be FAILED, but was not")
	}
}

// TestD108_AutoFailZombie_NoTicketLeg 腿无 ticket → 未命中 → 置 FAILED。
func TestD108_AutoFailZombie_NoTicketLeg(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 202, Status: "OPENING", OpportunityID: "SWAPARB:AUDJPY:vA:vB:1"}
	fs.positions[202] = pos
	// 腿无 OrderID → 跳过 → 未命中 → FAILED
	fs.legs[202] = []*pgstore.PositionLeg{
		{PositionID: 202, LegNo: 0, Phase: "OPEN", Venue: "vA:mt5", OrderID: "", Side: "BUY", Qty: 1.0, Status: "FILLED"},
	}
	venueOrders := map[string]VenueOrder{"T999": {Ticket: "T999"}}

	r := &Reconciler{store: fs, runTimeout: 5 * time.Second}
	if err := r.autoFailOPENINGZombie(context.Background(), pos, venueOrders); err != nil {
		t.Fatalf("autoFailOPENINGZombie: %v", err)
	}
	found := false
	for _, id := range fs.failedIDs {
		if id == 202 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no ticket leg: position should be FAILED, but was not")
	}
}
