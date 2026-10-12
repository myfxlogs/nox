// d417_zombie_test.go —— D-417 S1/S3：zombie 守卫 LegAtVenue 接入 + OPENING 推进分支测试（先红后绿）。
// ordersByVenue 新签名：map[venueKey][]exec.OpenedOrder（venue 存在键=查询成功，空切片=venue 无单）。
package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/trading/exec"
)

func d417Recon(fs *fakeStore) *Reconciler {
	return &Reconciler{store: fs, runTimeout: 5 * time.Second}
}

// TestD417_LeggedZombie_CEXVenueAlive CEX 腿 venue 存活（instId≠ordId）→ 不误杀 FAILED + warn。
func TestD417_LeggedZombie_CEXVenueAlive(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 300, Status: "LEGGED"}
	fs.positions[300] = pos
	fs.legs[300] = []*pgstore.PositionLeg{
		{PositionID: 300, LegNo: 0, Phase: "OPEN", Venue: "okx:perp", OrderID: "3947142706953965568",
			Symbol: "XAUT-USDT-SWAP", Side: "BUY", Status: "FILLED"},
		{PositionID: 300, LegNo: 1, Phase: "OPEN", Venue: "vB:mt5", OrderID: "T9", Status: "FAILED"},
	}
	orders := map[string][]exec.OpenedOrder{
		"okx:perp": {{Ticket: "XAUT-USDT-SWAP", Symbol: "XAUT-USDT-SWAP", Side: "BUY", PosSide: "long"}},
		"vB:mt5":   {},
	}
	if err := d417Recon(fs).autoFailLeggedZombie(context.Background(), pos, orders); err != nil {
		t.Fatalf("autoFailLeggedZombie: %v", err)
	}
	for _, id := range fs.failedIDs {
		if id == 300 {
			t.Fatal("CEX leg alive at venue: must NOT mark FAILED")
		}
	}
	found := false
	for _, a := range fs.alerts {
		if a.level == "warn" && strings.Contains(a.msg, "不归档为失败") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected warn '不归档为失败', got %v", fs.alerts)
	}
}

// TestD417_LeggedZombie_VenueQueryGap 腿 venue 未成功查询 → fail-closed 不动仓（非 FAILED）。
func TestD417_LeggedZombie_VenueQueryGap(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 301, Status: "LEGGED"}
	fs.positions[301] = pos
	fs.legs[301] = []*pgstore.PositionLeg{
		{PositionID: 301, LegNo: 0, Phase: "OPEN", Venue: "gate:perp", OrderID: "777",
			Symbol: "XAU_USDT", Side: "BUY", Status: "FILLED"},
	}
	// gate:perp 查询失败 → ordersByVenue 无该键
	if err := d417Recon(fs).autoFailLeggedZombie(context.Background(), pos, map[string][]exec.OpenedOrder{}); err != nil {
		t.Fatalf("autoFailLeggedZombie: %v", err)
	}
	if len(fs.statusCalls) != 0 {
		t.Fatalf("venue query gap must fail-closed no status change, got %v", fs.statusCalls)
	}
}

// TestD417_OPENINGZombie_AllLegsPresent §1.3b：全 OPEN 腿在场 → 推进 OPEN（非 warn 滞留）。
func TestD417_OPENINGZombie_AllLegsPresent(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 302, Status: "OPENING"}
	fs.positions[302] = pos
	fs.legs[302] = []*pgstore.PositionLeg{
		{PositionID: 302, LegNo: 0, Phase: "OPEN", Venue: "okx:perp", OrderID: "ord1",
			Symbol: "XAUT-USDT-SWAP", Side: "BUY", Status: "FILLED"},
		{PositionID: 302, LegNo: 1, Phase: "OPEN", Venue: "vB:mt5", OrderID: "T5",
			Symbol: "XAUUSD", Side: "SELL", Status: "FILLED"},
	}
	orders := map[string][]exec.OpenedOrder{
		"okx:perp": {{Ticket: "XAUT-USDT-SWAP", Symbol: "XAUT-USDT-SWAP", Side: "BUY", PosSide: "long"}},
		"vB:mt5":   {{Ticket: "T5", Symbol: "XAUUSD", Side: "SELL"}},
	}
	if err := d417Recon(fs).autoFailOPENINGZombie(context.Background(), pos, orders); err != nil {
		t.Fatalf("autoFailOPENINGZombie: %v", err)
	}
	if fs.statusCalls[302] != "OPEN" {
		t.Fatalf("all legs present → want OPEN, got statusCalls=%v pos=%s", fs.statusCalls, pos.Status)
	}
}

// TestD417_OPENINGZombie_PartialLegs §1.3b：部分腿在场 → 推进 LEGGED（进 D-414 收编域）。
func TestD417_OPENINGZombie_PartialLegs(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 303, Status: "OPENING"}
	fs.positions[303] = pos
	fs.legs[303] = []*pgstore.PositionLeg{
		{PositionID: 303, LegNo: 0, Phase: "OPEN", Venue: "okx:perp", OrderID: "ord1",
			Symbol: "XAUT-USDT-SWAP", Side: "BUY", Status: "FILLED"},
		{PositionID: 303, LegNo: 1, Phase: "OPEN", Venue: "vB:mt5", OrderID: "T5",
			Symbol: "XAUUSD", Side: "SELL", Status: "UNKNOWN"},
	}
	orders := map[string][]exec.OpenedOrder{
		"okx:perp": {{Ticket: "XAUT-USDT-SWAP", Symbol: "XAUT-USDT-SWAP", Side: "BUY", PosSide: "long"}},
		"vB:mt5":   {}, // venue 查空=腿不在
	}
	if err := d417Recon(fs).autoFailOPENINGZombie(context.Background(), pos, orders); err != nil {
		t.Fatalf("autoFailOPENINGZombie: %v", err)
	}
	if fs.statusCalls[303] != "LEGGED" {
		t.Fatalf("partial legs → want LEGGED, got %v", fs.statusCalls)
	}
}

// TestD417R1_OPENINGZombie_FailedNoOrderLeg R1：FILLED 腿在场 + FAILED 无单号腿 → LEGGED
// （FAILED 无单号腿计 total 不计 present，不能当全腿齐推 OPEN）。
func TestD417R1_OPENINGZombie_FailedNoOrderLeg(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 305, Status: "OPENING"}
	fs.positions[305] = pos
	fs.legs[305] = []*pgstore.PositionLeg{
		{PositionID: 305, LegNo: 0, Phase: "OPEN", Venue: "okx:perp", OrderID: "ord1",
			Symbol: "XAUT-USDT-SWAP", Side: "BUY", Status: "FILLED"},
		{PositionID: 305, LegNo: 1, Phase: "OPEN", Venue: "vB:mt5", OrderID: "",
			Symbol: "XAUUSD", Side: "SELL", Status: "FAILED"},
	}
	orders := map[string][]exec.OpenedOrder{
		"okx:perp": {{Ticket: "XAUT-USDT-SWAP", Symbol: "XAUT-USDT-SWAP", Side: "BUY", PosSide: "long"}},
	}
	if err := d417Recon(fs).autoFailOPENINGZombie(context.Background(), pos, orders); err != nil {
		t.Fatalf("autoFailOPENINGZombie: %v", err)
	}
	if fs.statusCalls[305] != "LEGGED" {
		t.Fatalf("FILLED 在场+FAILED 无单号 → want LEGGED, got %v", fs.statusCalls)
	}
}

// TestD417R1_LeggedZombie_FailedLegAtVenue R1：FAILED 带单号腿在 venue 在场 → warn 不归档。
func TestD417R1_LeggedZombie_FailedLegAtVenue(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 306, Status: "LEGGED"}
	fs.positions[306] = pos
	fs.legs[306] = []*pgstore.PositionLeg{
		{PositionID: 306, LegNo: 0, Phase: "OPEN", Venue: "okx:perp", OrderID: "ord9",
			Symbol: "XAUT-USDT-SWAP", Side: "BUY", Status: "FAILED"},
	}
	orders := map[string][]exec.OpenedOrder{
		"okx:perp": {{Ticket: "XAUT-USDT-SWAP", Symbol: "XAUT-USDT-SWAP", Side: "BUY", PosSide: "long"}},
	}
	if err := d417Recon(fs).autoFailLeggedZombie(context.Background(), pos, orders); err != nil {
		t.Fatalf("autoFailLeggedZombie: %v", err)
	}
	if len(fs.statusCalls) != 0 {
		t.Fatalf("FAILED 腿在 venue 在场 → warn 不归档，got statusCalls=%v", fs.statusCalls)
	}
	found := false
	for _, a := range fs.alerts {
		if a.level == "warn" && strings.Contains(a.msg, "不归档为失败") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected warn '不归档为失败', got %v", fs.alerts)
	}
}

// TestD417_OPENINGZombie_NoLegs 回归：venue 全无腿 → FAILED 归档（旧行为不破）。
func TestD417_OPENINGZombie_NoLegs(t *testing.T) {
	fs := newFakeStore()
	pos := &pgstore.Position{ID: 304, Status: "OPENING"}
	fs.positions[304] = pos
	fs.legs[304] = []*pgstore.PositionLeg{
		{PositionID: 304, LegNo: 0, Phase: "OPEN", Venue: "vA:mt5", OrderID: "T200", Status: "UNKNOWN"},
	}
	orders := map[string][]exec.OpenedOrder{"vA:mt5": {}}
	if err := d417Recon(fs).autoFailOPENINGZombie(context.Background(), pos, orders); err != nil {
		t.Fatalf("autoFailOPENINGZombie: %v", err)
	}
	if fs.statusCalls[304] != "FAILED" {
		t.Fatalf("no legs at venue → want FAILED, got %v", fs.statusCalls)
	}
}
