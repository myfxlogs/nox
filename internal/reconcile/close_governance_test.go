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
	stuckAt := func(d time.Duration) time.Time { return time.Now().Add(-d) }
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
			base := time.Now()
			for _, off := range tc.cycles {
				r.nowFn = func() time.Time { return base.Add(off) }
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
	p := mkPosition(601, "CLOSING", "CARRY", time.Now().Add(-3*time.Hour))
	u := "broker 返回矛盾"
	p.UncertaintyReason = &u
	fs.positions[601] = p
	r := New(fs)
	r.runTimeout = 5 * time.Second
	base := time.Now()
	for _, off := range []time.Duration{0, 10 * time.Minute, 70 * time.Minute} {
		r.nowFn = func() time.Time { return base.Add(off) }
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

// TestD245_ReconcileLeggedTriggerLogged：对账收尾触发源持久化（缺口2）。
// 直调 autoClose（镜像 TestAutoClose_HitAndCloseOtherLeg 形态）——单腿消失+History 命中+另一腿 OPEN。
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
		if tr.PositionID != nil && *tr.PositionID == 602 && tr.Status == "CLOSE_TRIGGER" && tr.Reason == "reconcile_legged" {
			found = true
		}
	}
	if !found {
		t.Fatalf("对账收尾未落 CLOSE_TRIGGER 日志（缺口2），closeTriggers=%v", fs.closeTriggers)
	}
}
