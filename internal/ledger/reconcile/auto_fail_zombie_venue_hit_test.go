// d108_p1_test.go —— D-108 P1 A3：僵尸循环死代码修复单测（先红后绿）。
// autoFailOPENINGZombie：venue 命中（腿在 venue 真有持仓）→ 不置 FAILED + warn 告警；
// 未命中才置 FAILED。
package reconcile

import (
	"context"
	"testing"
	"time"

	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// TestD108_AutoFailZombie_VenueHit 命中 venue（全腿在场）→ D-417 §1.3b 推进 OPEN。
func TestD108_AutoFailZombie_VenueHit(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 200, Status: "OPENING", OpportunityID: "SWAPARB:AUDJPY:vA:vB:1"}
	fs.positions[200] = pos
	// 腿有 ticket，且 venueOrders 含该 ticket = venue 命中
	fs.legs[200] = []*pgstore.PositionLeg{
		{PositionID: 200, LegNo: 0, Phase: "OPEN", Venue: "vA:mt5", OrderID: "T100", Side: "BUY", Qty: 1.0, Status: "FILLED"},
	}
	// 复合 key（venue_key+":"+ticket）
	orders := map[string][]exec.OpenedOrder{"vA:mt5": {{Ticket: "T100", Symbol: "AUDJPY", Side: "BUY", Volume: 1.0}}}

	r := &Reconciler{store: fs, runTimeout: 5 * time.Second}
	if err := r.autoFailOPENINGZombie(context.Background(), pos, orders); err != nil {
		t.Fatalf("autoFailOPENINGZombie: %v", err)
	}
	// §1.3b：全腿在场 → 推进 OPEN（非 FAILED 非滞留）
	if fs.statusCalls[200] != "OPEN" {
		t.Fatalf("venue hit all legs: want OPEN, got statusCalls=%v", fs.statusCalls)
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
	orders := map[string][]exec.OpenedOrder{"vA:mt5": {}} // venue 查空 = 未命中

	r := &Reconciler{store: fs, runTimeout: 5 * time.Second}
	if err := r.autoFailOPENINGZombie(context.Background(), pos, orders); err != nil {
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
	orders := map[string][]exec.OpenedOrder{"vA:mt5": {{Ticket: "T999"}}}

	r := &Reconciler{store: fs, runTimeout: 5 * time.Second}
	if err := r.autoFailOPENINGZombie(context.Background(), pos, orders); err != nil {
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
