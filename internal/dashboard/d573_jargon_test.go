package dashboard

// d573_jargon_test.go —— D-573 验收锚 A1/A2：飞书告警白话化（三健康族文案
// 重写为中文；数值/venue/时长白名单保留；状态机/去重逻辑零动）。

import (
	"context"
	"strings"
	"testing"

	"arb/internal/bus"
	"time"
)

// A1：mt-health 三态——warn/critical/info 全中文文案+venue/metric/age 保留。
func TestD573_A1_MTHealthCN(t *testing.T) {
	store := newFakeHealthStore()
	mon := NewMTHealthMonitor(&fakeVenueSource{}, bus.New(), store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour, 24*time.Hour, 72*time.Hour) // D-067 margin 阈值
	mon.now = func() time.Time { return wedUTC } // 非周末防静默窗
	ctx := context.Background()

	mon.maybeAlert(ctx, "okx", "quote", metricHealthy, metricWarn, 5*time.Minute)
	mon.maybeAlert(ctx, "okx", "quote", metricWarn, metricCrit, 30*time.Minute)
	mon.maybeAlert(ctx, "okx", "quote", metricCrit, metricHealthy, 30*time.Minute)

	a := store.snapshot()
	if len(a) != 3 {
		t.Fatalf("三态各一条: %d", len(a))
	}
	if a[0].level != "warn" || !strings.Contains(a[0].msg, "MT5 数据监控") ||
		!strings.Contains(a[0].msg, "okx") || !strings.Contains(a[0].msg, "行情报价") ||
		!strings.Contains(a[0].msg, "停更") || !strings.Contains(a[0].msg, "5m0s") {
		t.Fatalf("warn 文案非白话/丢数值: %q", a[0].msg)
	}
	if a[1].level != "critical" || !strings.Contains(a[1].msg, "严重停更") ||
		!strings.Contains(a[1].msg, "30m0s") {
		t.Fatalf("critical 文案: %q", a[1].msg)
	}
	if a[2].level != "info" || !strings.Contains(a[2].msg, "已恢复") {
		t.Fatalf("recovered 文案: %q", a[2].msg)
	}
	for _, x := range a {
		if strings.Contains(x.msg, "mt-health") || strings.Contains(x.msg, "stale") ||
			strings.Contains(x.msg, "recovered") || strings.Contains(x.msg, "age=") {
			t.Fatalf("黑话残留: %q", x.msg)
		}
	}
}

// A2：table-health 三态中文+表名/age 保留。
func TestD573_A2_TableHealthCN(t *testing.T) {
	tstore := newFakeTableHealthStore()
	mon := NewTableFreshnessMonitor(tstore, nil, time.Minute)
	mon.now = func() time.Time { return wedUTC }
	ctx := context.Background()

	mon.maybeTableAlert(ctx, "cex_spread", metricHealthy, metricWarn, 2*time.Hour)
	mon.maybeTableAlert(ctx, "cex_spread", metricWarn, metricCrit, 3*time.Hour)
	mon.maybeTableAlert(ctx, "cex_spread", metricCrit, metricHealthy, 3*time.Hour)

	a := tstore.alerts
	if len(a) != 3 {
		t.Fatalf("三态各一条: %d", len(a))
	}
	if a[0].level != "warn" || !strings.Contains(a[0].msg, "表格新鲜度监控") ||
		!strings.Contains(a[0].msg, "cex_spread") || !strings.Contains(a[0].msg, "停更") ||
		!strings.Contains(a[0].msg, "2h0m0s") {
		t.Fatalf("warn 文案: %q", a[0].msg)
	}
	if !strings.Contains(a[1].msg, "严重滞后") || !strings.Contains(a[2].msg, "已恢复") {
		t.Fatalf("crit/recover 文案: %q | %q", a[1].msg, a[2].msg)
	}
	for _, x := range a {
		if strings.Contains(x.msg, "table-health") || strings.Contains(x.msg, "age=") {
			t.Fatalf("黑话残留: %q", x.msg)
		}
	}
}
