// d159_w2a_test.go —— D-159 W2a：对账复合 key + CLOSING 扫描 + auto_close 收编单测。
// A6：两账户相同 ticket 不串仓（复合 key venue_key+":"+ticket）。
// A7：CLOSING 持仓超 30min → critical 告警。
// A8：auto_close otherOpen=false → 走 close coordinator（AutoClosePosition）。
package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/data/venue"
	"arb/internal/store/pgstore"
)

// TestReconcileCompositeKey A6：两账户相同 ticket，复合 key 不串仓。
// 账户 A（acctA:mt5）和账户 B（acctB:mt5）都有 ticket=123。
// venue querier 只返回 acctA 的订单 → A 匹配（正常），B 不匹配 → 查证 → 未命中 → critical。
// 旧逻辑（ticket-only key）：B 的 leg 会匹配 A 的 venue order（串仓），不告警（错误）。
func TestReconcileCompositeKey(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-1 * time.Hour)
	// 账户 A，ticket=123
	fs.positions[100] = mkPosition(100, "OPEN", "CROSS_VENUE", opened)
	fs.legs[100] = []*pgstore.PositionLeg{
		mkOpenLeg(100, 0, "acctA:mt5", "123", "BUY", "EURUSD", 1.0),
	}
	// 账户 B，相同 ticket=123（不同 venue）
	fs.positions[101] = mkPosition(101, "OPEN", "CROSS_VENUE", opened)
	fs.legs[101] = []*pgstore.PositionLeg{
		mkOpenLeg(101, 0, "acctB:mt5", "123", "BUY", "EURUSD", 1.0),
	}

	// venue querier 只返回 acctA 的订单（ticket=123, VenueKey=acctA:mt5）
	vq := &fakeVenueQuerier{
		venueKey: "acctA:mt5",
		orders:   []venue.Order{{Ticket: "123", VenueKey: "acctA:mt5", Symbol: "EURUSD", Side: "BUY", Volume: 1.0}},
	}
	// acctB 无 venue querier，但有 history querier（返回 nil=未命中）
	hq := &fakeHistoryQuerier{venueKey: "acctB:mt5", results: map[string]*HistoryOrder{}}

	r := New(fs, vq)
	r.SetOrderHistoryQueriers(hq)
	r.runTimeout = 5 * time.Second

	mismatches, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 账户 B 的 leg 在 venue 侧未找到（复合 key 不串仓）→ 历史查证未命中 → critical
	if mismatches != 1 {
		t.Errorf("mismatches=%d，期望 1（账户 B 未匹配）", mismatches)
	}
	if !hasAlert(fs, "critical", "真失联") {
		t.Errorf("缺少 critical「真失联」告警（账户 B ticket=123 应在 acctB 侧未找到），alerts=%v", fs.alerts)
	}
	// 账户 A 不应有 critical（匹配成功）
	for _, a := range fs.alerts {
		if a.level == "critical" && strings.Contains(a.msg, "#100") {
			t.Errorf("账户 A 不应告警（匹配成功），但收到: %s", a.msg)
		}
	}
}

// TestReconcileCLOSING_Stuck A7：CLOSING 持仓超 30min → critical 告警。
func TestReconcileCLOSING_Stuck(t *testing.T) {
	fs := newFakeStore()
	// CLOSING 超 30min
	old := time.Now().Add(-31 * time.Minute)
	fs.positions[200] = mkPosition(200, "CLOSING", "CROSS_VENUE", old)

	r := New(fs)
	r.runTimeout = 5 * time.Second

	_, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 告警消息改为 "CLOSING position #200 超...自动重触发 ResumeClose"。
	if !hasAlert(fs, "critical", "CLOSING position #200") {
		t.Errorf("缺少 CLOSING 超时 critical 告警，alerts=%v", fs.alerts)
	}
}

// TestReconcileCLOSING_NotStuck A7：CLOSING 持仓未超 30min → 不告警。
func TestReconcileCLOSING_NotStuck(t *testing.T) {
	fs := newFakeStore()
	recent := time.Now().Add(-10 * time.Minute)
	fs.positions[201] = mkPosition(201, "CLOSING", "CROSS_VENUE", recent)

	r := New(fs)
	r.runTimeout = 5 * time.Second

	_, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, a := range fs.alerts {
		if a.level == "critical" && strings.Contains(a.msg, "CLOSING 持仓") {
			t.Errorf("未超 30min 不应告警，但收到: %s", a.msg)
		}
	}
}

// TestIsClosingStuck A7：isClosingStuck 边界判定。
// 改用 ClosingStartedAt（不再用 OpenedAt）。
func TestIsClosingStuck(t *testing.T) {
	now := time.Now()
	// 超 30min → 卡住
	old := now.Add(-31 * time.Minute)
	pos := &pgstore.Position{Status: "CLOSING", ClosingStartedAt: &old}
	if !isClosingStuck(pos, now) {
		t.Error("CLOSING >30min: should be stuck")
	}
	// 未超 30min → 不卡
	recent := now.Add(-10 * time.Minute)
	pos2 := &pgstore.Position{Status: "CLOSING", ClosingStartedAt: &recent}
	if isClosingStuck(pos2, now) {
		t.Error("CLOSING <30min: should not be stuck")
	}
	// 无 closing_started_at → 异常=卡住
	pos3 := &pgstore.Position{Status: "CLOSING", ClosingStartedAt: nil}
	if !isClosingStuck(pos3, now) {
		t.Error("CLOSING no closing_started_at: should be stuck (anomaly)")
	}
	// 非 CLOSING → 不判
	pos4 := &pgstore.Position{Status: "OPEN", ClosingStartedAt: &old}
	if isClosingStuck(pos4, now) {
		t.Error("non-CLOSING: should not be stuck")
	}
}

// TestAutoCloseCoordinator A8：auto_close otherOpen=false → 走 close coordinator（AutoClosePosition）。
// 场景：position #300 单腿，venue 侧已平（历史单命中），otherOpen=false。
// 期望：调 closer.AutoClosePosition（不独立调 store.ClosePosition）。
func TestAutoCloseCoordinator(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-2 * time.Hour)
	fs.positions[300] = mkPosition(300, "OPEN", "CROSS_VENUE", opened)
	fs.legs[300] = []*pgstore.PositionLeg{
		mkOpenLeg(300, 0, "52993526:mt5", "Y", "BUY", "GBPUSD", 1.0),
	}

	fc := &fakeCloser{}
	fsst := &fakeSettler{}
	r := &Reconciler{
		store:   fs,
		closer:  fc,
		settler: fsst,
	}

	hist := &HistoryOrder{Ticket: "Y", ClosePrice: 1.3, Profit: 5.0, Swap: 0, Commission: 0, Symbol: "GBPUSD"}
	goneLeg := *mkOpenLeg(300, 0, "52993526:mt5", "Y", "BUY", "GBPUSD", 1.0)

	if err := r.autoClose(context.Background(), fs.positions[300], goneLeg, hist); err != nil {
		t.Fatalf("autoClose: %v", err)
	}
	// 走 close coordinator（AutoClosePosition），不独立调 store.ClosePosition
	if len(fc.calls) != 1 || fc.calls[0] != 300 {
		t.Errorf("closer AutoClosePosition 调用=%v，期望 [300]", fc.calls)
	}
	// store.ClosePosition 不应被直接调用（由 AutoClosePosition 内部调 pipeline.ClosePosition）
	if fs.closeCalls != 0 {
		t.Errorf("store.ClosePosition 直接调用次数=%d，期望 0（应走 coordinator）", fs.closeCalls)
	}
}

// TestAutoCloseCoordinator_ClaimFailedFallback A8 / D-189 P0-2：ResumeClose 返回 ErrClaimFailed → 静默处理（不独立调 store.ClosePosition）。
// 场景：position 已是 CLOSING（被并发领取），ResumeClose 返回 ErrClaimFailed。
// 期望：ResumeClose 被调用，ErrClaimFailed 静默处理，store.ClosePosition 不被直接调用（D-189 P0-2 删除 raw 旁路）。
func TestAutoCloseCoordinator_ClaimFailedFallback(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-2 * time.Hour)
	fs.positions[300] = mkPosition(300, "OPEN", "CROSS_VENUE", opened)
	fs.legs[300] = []*pgstore.PositionLeg{
		mkOpenLeg(300, 0, "52993526:mt5", "Y", "BUY", "GBPUSD", 1.0),
	}

	// fakeCloser 返回 ErrClaimFailed（模拟 position 已是 CLOSING）
	fc := &fakeCloser{claimFailed: true}
	r := &Reconciler{
		store:  fs,
		closer: fc,
	}

	hist := &HistoryOrder{Ticket: "Y", ClosePrice: 1.3, Profit: 5.0, Swap: 0, Commission: 0, Symbol: "GBPUSD"}
	goneLeg := *mkOpenLeg(300, 0, "52993526:mt5", "Y", "BUY", "GBPUSD", 1.0)

	if err := r.autoClose(context.Background(), fs.positions[300], goneLeg, hist); err != nil {
		t.Fatalf("autoClose: %v", err)
	}
	// ResumeClose 被调用（fakeCloser.calls 记录 AutoClosePosition+ResumeClose 调用）
	if len(fc.calls) == 0 {
		t.Errorf("closer ResumeClose 未被调用，期望至少 1 次")
	}
	// store.ClosePosition 不应被直接调用（D-189 P0-2 删除 raw 旁路，由 ResumeClose 内部处理）
	if fs.closeCalls != 0 {
		t.Errorf("store.ClosePosition 直接调用次数=%d，期望 0（D-189 P0-2 删除 raw 旁路）", fs.closeCalls)
	}
}

// TestAutoCloseSettleFailed_NoteMarked S7：settlement 失败 → position.note 标记 settle_failed。
func TestAutoCloseSettleFailed_NoteMarked(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-2 * time.Hour)
	fs.positions[300] = mkPosition(300, "OPEN", "CROSS_VENUE", opened)
	fs.legs[300] = []*pgstore.PositionLeg{
		mkOpenLeg(300, 0, "52993526:mt5", "Y", "BUY", "GBPUSD", 1.0),
	}

	fc := &fakeCloser{}
	// fakeSettler 返回 error
	fsst := &fakeSettler{fail: true}
	r := &Reconciler{
		store:   fs,
		closer:  fc,
		settler: fsst,
	}

	hist := &HistoryOrder{Ticket: "Y", ClosePrice: 1.3, Profit: 5.0, Swap: 0, Commission: 0, Symbol: "GBPUSD"}
	goneLeg := *mkOpenLeg(300, 0, "52993526:mt5", "Y", "BUY", "GBPUSD", 1.0)

	_ = r.autoClose(context.Background(), fs.positions[300], goneLeg, hist)
	// position.note 应标记 settle_failed
	if !strings.HasPrefix(fs.positions[300].Note, "settle_failed:") {
		t.Errorf("position.note=%q，期望以 'settle_failed:' 开头", fs.positions[300].Note)
	}
}
