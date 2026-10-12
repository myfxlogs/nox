package dashboard

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
)

// D-136 修复②：isMTWeekendQuietWindow MT 周末休市静默窗（Fri 20:55 UTC → Sun 22:10 UTC）。
// 先红后绿：旧代码无此函数=编译红即先红证据。

func utcTime(wd time.Weekday, h, m int) time.Time {
	// 以 2026-08 为基准构造指定星期几的 UTC 时刻；2026-08-26=Wednesday。
	base := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC) // Wednesday
	offset := int(wd - time.Wednesday)
	if offset < 0 {
		offset += 7
	}
	return base.AddDate(0, 0, offset).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

func TestIsMTWeekendQuietWindow(t *testing.T) {
	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"Fri 20:50 (收盘前5min外)", utcTime(time.Friday, 20, 50), false},
		{"Fri 21:00 (收盘后)", utcTime(time.Friday, 21, 0), true},
		{"Sat 12:00 (周末中段)", utcTime(time.Saturday, 12, 0), true},
		{"Sun 21:55 (开盘前15min)", utcTime(time.Sunday, 21, 55), true},
		{"Sun 22:15 (开盘后5min)", utcTime(time.Sunday, 22, 15), false},
		{"Mon 10:00 (交易日)", utcTime(time.Monday, 10, 0), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isMTWeekendQuietWindow(c.t); got != c.want {
				t.Fatalf("isMTWeekendQuietWindow(%s)=%v, want %v", c.t.Format("Mon 15:04 MST"), got, c.want)
			}
		})
	}
}

// D-170：quietNow 三层——①表缺席回退粗窗（周六=quiet）；②session 闭市判=quiet；
// ③session 判开市=不 quiet（粗窗静默语义被 session 真值覆盖，周六开市品种不误杀）。
func TestD170_QuietNowSessionGate(t *testing.T) {
	sat := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) // Saturday
	store := newFakeHealthStore()
	mon := &MTHealthMonitor{store: store, now: func() time.Time { return sat }}
	ctx := context.Background()
	// ① 表缺席 → 回退粗窗：周六=quiet。
	if !mon.quietNow(ctx, "vA") {
		t.Fatal("表缺席+周六应 quiet（粗窗兜底）")
	}
	// ② session 判闭（全周日窗）→ quiet。
	store.facts[fact.KindTradeSession] = []fact.Fact{
		{Venue: "vA", Symbol: "XAU", Value: 0}, // wday=0(Sun) 全天窗
	}
	if !mon.quietNow(ctx, "vA") {
		t.Fatal("session 全周日窗应 quiet")
	}
	// ③ session 判开市（周六全天窗）→ 不 quiet。
	store.facts[fact.KindTradeSession] = []fact.Fact{
		{Venue: "vA", Symbol: "XAU", Value: float64(6 * 1e8)}, // wday=6(Sat) 全天窗
	}
	if mon.quietNow(ctx, "vA") {
		t.Fatal("session 周六开市不应 quiet（粗窗不再拦）")
	}
}
