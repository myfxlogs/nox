// close_governance_test.go —— D-245 休市窗平仓治理（先红后绿）。
// 缺口1：CLOSING 看护告警按时长分层（<2h critical 每循环 / ≥2h warn 每小时至多 1 条，F4 日历无关——按时长即可覆盖休市）。
// 缺口2：对账收尾触发源持久化（InsertAutotradeLog CLOSE_TRIGGER / reconcile_legged）。
package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// D-234批1 R-F1：固定基点除墙钟 flaky——runtime 小时桶按墙钟去重，time.Now() 基点分钟 ≥50 时
// +10min 用例跨小时桶必败；分钟须 <50（+10min 用例留同小时桶余量）。
var d245Base = time.Date(2026, 1, 5, 0, 5, 0, 0, time.UTC)

// countAlerts 统计 fakeStore 中匹配 level+子串的告警条数。
func countAlerts(fs *fakeStore, level, substr string) int {
	n := 0
	for _, a := range fs.alerts {
		if a.level == level && strings.Contains(a.msg, substr) {
			n++
		}
	}
	return n
}

// TestD245_ClosingAlertTiering：假时钟表驱动——告警分层与小时桶去重。
func TestD245_ClosingAlertTiering(t *testing.T) {
	// D-234批1 R-F1：固定基点除墙钟 flaky（runtime 小时桶按墙钟去重，time.Now() 基点分钟 ≥50 时
	// +10min 用例跨小时桶必败）；分钟须 <50（+10min 用例留同小时桶余量）。
	base := time.Date(2026, 1, 5, 0, 5, 0, 0, time.UTC)
	stuckAt := func(d time.Duration) time.Time { return base.Add(-d) }
	cases := []struct {
		name     string
		stuck    time.Duration   // closing_started_at 距今
		cycles   []time.Duration // 每循环的墙钟（相对 now 基点）
		wantCrit int
		wantWarn int
	}{
		{"卡死1h两循环→两条critical", 1 * time.Hour, []time.Duration{0, 30 * time.Minute}, 2, 0},
		{"卡死3h同小时两循环→一条warn", 3 * time.Hour, []time.Duration{0, 10 * time.Minute}, 0, 1},
		{"卡死3h跨小时→两条warn", 3 * time.Hour, []time.Duration{0, 70 * time.Minute}, 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeStore()
			fs.positions[600] = mkPosition(600, "CLOSING", "CARRY", stuckAt(tc.stuck))
			r := New(fs)
			r.runTimeout = 5 * time.Second
			for _, off := range tc.cycles {
				r.nowFn = func() time.Time { return d245Base.Add(off) }
				if _, err := r.Run(context.Background()); err != nil {
					t.Fatalf("Run: %v", err)
				}
			}
			// 终计数（fakeStore.alerts 累积全量，循环内累加会重复计数）
			crit := countAlerts(fs, "critical", "CLOSING position #600")
			warn := countAlerts(fs, "warn", "CLOSING position #600")
			if crit != tc.wantCrit || warn != tc.wantWarn {
				t.Fatalf("critical=%d warn=%d, want %d/%d", crit, warn, tc.wantCrit, tc.wantWarn)
			}
		})
	}
}

// TestD245_UnknownTiering：UNKNOWN 分支同规则（≥2h 降 warn 每小时至多 1 条）。
func TestD245_UnknownTiering(t *testing.T) {
	fs := newFakeStore()
	p := mkPosition(601, "CLOSING", "CARRY", d245Base.Add(-3*time.Hour)) // D-234批1 R-F1：同固定基点
	u := "broker 返回矛盾"
	p.UncertaintyReason = &u
	fs.positions[601] = p
	r := New(fs)
	r.runTimeout = 5 * time.Second
	for _, off := range []time.Duration{0, 10 * time.Minute, 70 * time.Minute} {
		r.nowFn = func() time.Time { return d245Base.Add(off) }
		if _, err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	crit := countAlerts(fs, "critical", "CLOSING UNKNOWN position #601")
	warn := countAlerts(fs, "warn", "CLOSING UNKNOWN position #601")
	if crit != 0 || warn != 2 {
		t.Fatalf("UNKNOWN 分层: critical=%d warn=%d, want 0/2", crit, warn)
	}
}

// TestD245_ReconcileLeggedTriggerLogged：对账收尾触发源持久化（缺口2 + D-249 reason 增强）。
// 直调 autoClose（镜像 TestAutoClose_HitAndCloseOtherLeg 形态）——单腿消失+History 命中+另一腿 OPEN。
// D-249 S1 第1项：reason 须含 venue/ticket/History 依据（不再只是裸 "reconcile_legged"）。
func TestD245_ReconcileLeggedTriggerLogged(t *testing.T) {
	fs := newFakeStore()
	opened := time.Now().Add(-2 * time.Hour)
	fs.positions[602] = mkPosition(602, "OPEN", "CARRY", opened)
	fs.legs[602] = []*pgstore.PositionLeg{
		mkOpenLeg(602, 0, "52993526:mt5", "A", "BUY", "AUDJPY", 1.0),
		mkOpenLeg(602, 1, "277842155:mt5", "B", "SELL", "AUDJPY", 1.0),
	}
	fc := &fakeCloser{}
	r := &Reconciler{store: fs, closer: fc, settler: &fakeSettler{}, closingAlertHour: map[int64]int64{}, nowFn: time.Now}

	hist := &HistoryOrder{Ticket: "A", ClosePrice: 100.5, Profit: 10.0, Swap: 0.5, Commission: 1.0, Symbol: "AUDJPY"}
	goneLeg := *mkOpenLeg(602, 0, "52993526:mt5", "A", "BUY", "AUDJPY", 1.0)

	if err := r.autoClose(context.Background(), fs.positions[602], goneLeg, hist); err != nil {
		t.Fatalf("autoClose: %v", err)
	}
	found := false
	for _, tr := range fs.closeTriggers {
		if tr.PositionID != nil && *tr.PositionID == 602 && tr.Status == "CLOSE_TRIGGER" &&
			strings.Contains(tr.Reason, "venue=") && strings.Contains(tr.Reason, "ticket=") {
			found = true
		}
	}
	if !found {
		t.Fatalf("对账收尾未落 CLOSE_TRIGGER 日志（reason 须含 venue/ticket），closeTriggers=%v", fs.closeTriggers)
	}
}

// TestD249_ResumeCloseTriggerLogged：D-249 S1 第2项——CLOSING 超时重触发 ResumeClose 持久化。
// 构造 CLOSING 卡死超 30min → Run → ResumeClose 调用前须落 RESUME_CLOSE 行，reason 含卡死时长。
func TestD249_ResumeCloseTriggerLogged(t *testing.T) {
	fs := newFakeStore()
	stuckAt := d245Base.Add(-35 * time.Minute) // 卡死 35min（>30min 阈值）
	fs.positions[603] = mkPosition(603, "CLOSING", "CARRY", stuckAt)
	fs.positions[603].ClosingStartedAt = &stuckAt
	fc := &fakeCloser{store: fs}
	r := &Reconciler{store: fs, closer: fc, closingAlertHour: map[int64]int64{}, nowFn: func() time.Time { return d245Base }}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	found := false
	for _, tr := range fs.closeTriggers {
		if tr.PositionID != nil && *tr.PositionID == 603 && tr.Status == "RESUME_CLOSE" &&
			strings.Contains(tr.Reason, "stuck_for=") {
			found = true
		}
	}
	if !found {
		t.Fatalf("CLOSING 超时重触发未落 RESUME_CLOSE 日志（reason 须含 stuck_for=），closeTriggers=%v", fs.closeTriggers)
	}
}
