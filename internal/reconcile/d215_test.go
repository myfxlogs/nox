// d215_test.go —— D-215：对账「本地未记录」对 CLOSING/LEGGED 持仓误报修复。
// 抑制集合 nonOpenLegKeys（CLOSING+LEGGED 持仓的 OPEN-phase 腿复合 key）让孤儿检查跳过在途腿，
// 不并入 localLegs（autoClose 对 CLOSING 的 ClaimClose 必拒→死循环）。
// 四测试：CLOSING 在途腿不误报 / LEGGED 在途腿不误报 / 真孤儿仍告警 / 复合 key 不串账户。
package reconcile

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// TestD215_CLOSINGLegInVenue_NoOrphanAlert：CLOSING 持仓（未超 30min 不触发 stuck）+ OPEN-phase
// FILLED 腿（acctA:mt5, ticket=123）+ venue 返回同票 → 不发「本地未记录」critical，mismatches==0。
// 修复前必红：localLegs 只从 OPEN 构建，CLOSING 腿不在集合 → 孤儿检查误报失联。本测试=bug 回归锚。
func TestD215_CLOSINGLegInVenue_NoOrphanAlert(t *testing.T) {
	fs := newFakeStore()
	closingStart := time.Now().Add(-10 * time.Minute) // <30min，不触发 isClosingStuck
	fs.positions[500] = mkPosition(500, "CLOSING", "CROSS_VENUE", closingStart)
	fs.legs[500] = []*pgstore.PositionLeg{
		mkOpenLeg(500, 0, "acctA:mt5", "123", "BUY", "EURUSD", 1.0),
	}
	vq := &fakeVenueQuerier{
		venueKey: "acctA:mt5",
		orders:   []VenueOrder{{Ticket: "123", VenueKey: "acctA:mt5", Symbol: "EURUSD", Side: "BUY", Volume: 1.0, Comment: "arb"}},
	}
	r := New(fs, vq)
	r.runTimeout = 5 * time.Second

	mm, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mm != 0 {
		t.Errorf("mismatches=%d, want 0（CLOSING 在途腿不应误报失联）", mm)
	}
	if hasAlert(fs, "critical", "本地未记录") {
		t.Errorf("不应发「本地未记录」critical（CLOSING 在途腿非失联），alerts=%v", fs.alerts)
	}
}

// TestD215_LEGGEDLegInVenue_NoOrphanAlert：LEGGED 持仓（-2h 未超 24h 不触发僵尸收编）+ FILLED 腿
// + venue 同票 → 无 orphan critical、无僵尸归档（position 仍 LEGGED）。
func TestD215_LEGGEDLegInVenue_NoOrphanAlert(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-2 * time.Hour) // <24h，不触发 isLeggedZombie
	fs.positions[510] = mkPosition(510, "LEGGED", "CROSS_VENUE", opened)
	fs.legs[510] = []*pgstore.PositionLeg{
		mkOpenLeg(510, 0, "acctA:mt5", "456", "BUY", "EURUSD", 1.0),
	}
	vq := &fakeVenueQuerier{
		venueKey: "acctA:mt5",
		orders:   []VenueOrder{{Ticket: "456", VenueKey: "acctA:mt5", Symbol: "EURUSD", Side: "BUY", Volume: 1.0, Comment: "arb"}},
	}
	r := New(fs, vq)
	r.runTimeout = 5 * time.Second

	mm, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mm != 0 {
		t.Errorf("mismatches=%d, want 0（LEGGED 在途腿不应误报失联）", mm)
	}
	if hasAlert(fs, "critical", "本地未记录") {
		t.Errorf("不应发「本地未记录」critical（LEGGED 在途腿非失联），alerts=%v", fs.alerts)
	}
	if p := fs.positions[510]; p.Status != "LEGGED" {
		t.Errorf("position #510 status=%s, want LEGGED（未超 24h 不应被僵尸收编）", p.Status)
	}
}

// TestD215_TrueOrphan_StillAlerts：无任何本地持仓，venue 返回 Comment="arb"（isARBOrder 命中）的
// ticket=999 → critical「本地未记录」照发、mismatches==1。防抑制面扩大（真孤儿必须仍告警）。
func TestD215_TrueOrphan_StillAlerts(t *testing.T) {
	fs := newFakeStore()
	vq := &fakeVenueQuerier{
		venueKey: "acctA:mt5",
		orders:   []VenueOrder{{Ticket: "999", VenueKey: "acctA:mt5", Symbol: "EURUSD", Side: "BUY", Volume: 1.0, Comment: "arb"}},
	}
	r := New(fs, vq)
	r.runTimeout = 5 * time.Second

	mm, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mm != 1 {
		t.Errorf("mismatches=%d, want 1（真孤儿应告警）", mm)
	}
	if !hasAlert(fs, "critical", "本地未记录") {
		t.Errorf("缺少 critical「本地未记录」（真孤儿必须仍告警），alerts=%v", fs.alerts)
	}
}

// TestD215_SuppressionUsesCompositeKey：CLOSING 持仓腿 key=acctA:mt5:123，venue 返回
// acctB:mt5:123（Comment="arb"，同票不同账户）→ 仍告警。抑制集合同构复合 key（venue_key+":"+ticket），
// 不串账户——同 ticket 跨账户的 venue 单不被误抑制。
func TestD215_SuppressionUsesCompositeKey(t *testing.T) {
	fs := newFakeStore()
	closingStart := time.Now().Add(-10 * time.Minute)
	fs.positions[520] = mkPosition(520, "CLOSING", "CROSS_VENUE", closingStart)
	fs.legs[520] = []*pgstore.PositionLeg{
		mkOpenLeg(520, 0, "acctA:mt5", "123", "BUY", "EURUSD", 1.0),
	}
	// venue querier 返回 acctB 的同票订单（与 CLOSING 腿的 acctA 复合 key 不同）
	vq := &fakeVenueQuerier{
		venueKey: "acctB:mt5",
		orders:   []VenueOrder{{Ticket: "123", VenueKey: "acctB:mt5", Symbol: "EURUSD", Side: "BUY", Volume: 1.0, Comment: "arb"}},
	}
	r := New(fs, vq)
	r.runTimeout = 5 * time.Second

	mm, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mm != 1 {
		t.Errorf("mismatches=%d, want 1（同票不同账户应告警，复合 key 不串账户）", mm)
	}
	if !hasAlert(fs, "critical", "本地未记录") {
		t.Errorf("缺少 critical「本地未记录」（acctB:123 不应被 acctA:123 抑制），alerts=%v", fs.alerts)
	}
}
