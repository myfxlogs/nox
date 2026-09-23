// d108_p2_test.go —— D-108 P2 T3：LEGGED 僵尸收编单测（先红后绿）。
// autoFailLeggedZombie：FILLED 腿在 venue 有 → warn 不归档（交人工/对账）；
// 无 FILLED 腿在 venue → FAILED 归档 + warn。
package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// TestD108P2_LeggedZombie_VenueHit FILLED 腿在 venue 有 → 不置 FAILED + warn 告警。
func TestD108P2_LeggedZombie_VenueHit(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 300, Status: "LEGGED", OpportunityID: "SWAPARB:AUDJPY:vA:vB:1"}
	fs.positions[300] = pos
	fs.legs[300] = []*pgstore.PositionLeg{
		{PositionID: 300, LegNo: 0, Phase: "OPEN", Venue: "vA:mt5", OrderID: "T300", Side: "BUY", Qty: 1.0, Status: "FILLED"},
		{PositionID: 300, LegNo: 1, Phase: "OPEN", Venue: "vB:mt5", OrderID: "", Side: "SELL", Qty: 1.0, Status: "FAILED"},
	}
	// D-159 W2a R3-7：复合 key（venue_key+":"+ticket）
	orders := map[string][]exec.OpenedOrder{"vA:mt5": {{Ticket: "T300", Symbol: "AUDJPY", Side: "BUY", Volume: 1.0}}}

	r := &Reconciler{store: fs, runTimeout: 5 * time.Second}
	if err := r.autoFailLeggedZombie(context.Background(), pos, orders); err != nil {
		t.Fatalf("autoFailLeggedZombie: %v", err)
	}
	// 不应置 FAILED
	for _, id := range fs.failedIDs {
		if id == 300 {
			t.Fatal("venue hit: LEGGED position should NOT be FAILED, but was")
		}
	}
	// 应有 warn 告警
	found := false
	for _, a := range fs.alerts {
		if a.level == "warn" && strings.Contains(a.msg, "LEGGED 僵尸") && strings.Contains(a.msg, "不归档") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("venue hit: expected warn alert 'LEGGED 僵尸...不归档', got: %v", fs.alerts)
	}
}

// TestD108P2_LeggedZombie_VenueMiss 无 FILLED 腿在 venue → 置 FAILED + warn 告警。
func TestD108P2_LeggedZombie_VenueMiss(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 301, Status: "LEGGED", OpportunityID: "SWAPARB:AUDJPY:vA:vB:1"}
	fs.positions[301] = pos
	fs.legs[301] = []*pgstore.PositionLeg{
		{PositionID: 301, LegNo: 0, Phase: "OPEN", Venue: "vA:mt5", OrderID: "T301", Side: "BUY", Qty: 1.0, Status: "FILLED"},
		{PositionID: 301, LegNo: 1, Phase: "OPEN", Venue: "vB:mt5", OrderID: "", Side: "SELL", Qty: 1.0, Status: "FAILED"},
	}
	orders := map[string][]exec.OpenedOrder{"vA:mt5": {}} // venue 查空 = 未命中

	r := &Reconciler{store: fs, runTimeout: 5 * time.Second}
	if err := r.autoFailLeggedZombie(context.Background(), pos, orders); err != nil {
		t.Fatalf("autoFailLeggedZombie: %v", err)
	}
	// 应置 FAILED
	found := false
	for _, id := range fs.failedIDs {
		if id == 301 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("venue miss: LEGGED position should be FAILED, but was not")
	}
	// 应有 warn 告警
	warnFound := false
	for _, a := range fs.alerts {
		if a.level == "warn" && strings.Contains(a.msg, "LEGGED 僵尸自动归档") {
			warnFound = true
			break
		}
	}
	if !warnFound {
		t.Fatalf("venue miss: expected warn alert 'LEGGED 僵尸自动归档', got: %v", fs.alerts)
	}
}

// TestD108P2_LeggedZombie_NoFilledLeg 无 FILLED 腿（全 FAILED）→ 未命中 → FAILED。
func TestD108P2_LeggedZombie_NoFilledLeg(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 302, Status: "LEGGED", OpportunityID: "SWAPARB:AUDJPY:vA:vB:1"}
	fs.positions[302] = pos
	// 全 FAILED 腿（无 FILLED）→ 循环跳过 → 未命中 → FAILED
	fs.legs[302] = []*pgstore.PositionLeg{
		{PositionID: 302, LegNo: 0, Phase: "OPEN", Venue: "vA:mt5", OrderID: "T302", Side: "BUY", Qty: 1.0, Status: "FAILED"},
	}
	orders := map[string][]exec.OpenedOrder{"vA:mt5": {{Ticket: "T302"}}}

	r := &Reconciler{store: fs, runTimeout: 5 * time.Second}
	if err := r.autoFailLeggedZombie(context.Background(), pos, orders); err != nil {
		t.Fatalf("autoFailLeggedZombie: %v", err)
	}
	found := false
	for _, id := range fs.failedIDs {
		if id == 302 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no filled leg: position should be FAILED, but was not")
	}
}

// TestD108P2_IsLeggedZombie 超时判定。
func TestD108P2_IsLeggedZombie(t *testing.T) {
	now := time.Now()
	// 超 24h → 僵尸
	old := now.Add(-25 * time.Hour)
	pos := &pgstore.Position{Status: "LEGGED", OpenedAt: &old}
	if !isLeggedZombie(pos, now) {
		t.Error("LEGGED >24h: should be zombie")
	}
	// 未超 24h → 非僵尸
	recent := now.Add(-1 * time.Hour)
	pos2 := &pgstore.Position{Status: "LEGGED", OpenedAt: &recent}
	if isLeggedZombie(pos2, now) {
		t.Error("LEGGED <24h: should not be zombie")
	}
	// 非 LEGGED → 非僵尸
	pos3 := &pgstore.Position{Status: "OPEN", OpenedAt: &old}
	if isLeggedZombie(pos3, now) {
		t.Error("non-LEGGED: should not be legged zombie")
	}
}
