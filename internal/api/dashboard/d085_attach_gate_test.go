// d085_attach_gate_test.go —— D-085 锚：MT venue attach 未就绪闸。
// T1 connected 但未 attach → 6 tick 零告警、quote=healthy（attaching 门内不判三态）；
// T2 attach 完成后仍无帧 → 恢复既有分类（flash-miss 后 crit）——gate 只在 attach 前生效；
// T3 attaching 持续 >15min → 恰一条 warn 含「attach」，零 critical。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/ledger/reconcile"
)

// d085Mon 组装 MTHealthMonitor：statuses 注入、bootGrace 清零（告警立即可发面）、时钟注入。
func d085Mon(store *fakeHealthStore, vs *fakeVenueSource, now *time.Time) *MTHealthMonitor {
	mon := NewMTHealthMonitor(vs, bus.New(), store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour, 24*time.Hour, 72*time.Hour)
	mon.bootGraceTicks = 0
	mon.now = func() time.Time { return *now }
	return mon
}

// T1：status=connected 但 querier 未 attach → attaching 门内：6 tick 零告警、状态 healthy。
func TestD085_AttachingNoAlerts(t *testing.T) {
	store := newFakeHealthStore()
	store.accountStatuses = map[string]string{"vA": "connected"}
	vs := &fakeVenueSource{} // 零 querier=未 attach
	cur := wedUTC
	mon := d085Mon(store, vs, &cur)
	for i := 0; i < 6; i++ {
		mon.poll(context.Background())
		cur = cur.Add(time.Minute)
	}
	if got := store.alertCount(); got != 0 {
		t.Fatalf("attaching 期间应零告警, got %d: %v", got, store.snapshot())
	}
	mon.mu.Lock()
	st := mon.states["vA"]
	mon.mu.Unlock()
	if st.quote != metricHealthy || st.swap != metricHealthy || st.margin != metricHealthy {
		t.Fatalf("attaching 三指标应 healthy, got %+v", st)
	}
}

// T2：attaching → querier 就绪后仍无帧 → 恢复正常分类（flash-miss 3 连后 crit）。
// 证明 gate 只在 attach 前生效，不遮蔽真停更。
func TestD085_AttachThenStaleStillCrits(t *testing.T) {
	store := newFakeHealthStore()
	store.accountStatuses = map[string]string{"vA": "connected"}
	vs := &fakeVenueSource{}
	cur := wedUTC
	mon := d085Mon(store, vs, &cur)
	mon.poll(context.Background())                                               // attaching：健康记录
	vs.queriers = []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vA:mt5"}} // attach 完成
	for i := 0; i < 4; i++ {                                                     // flash-miss 3 连后第 3 tick 放行 crit
		cur = cur.Add(time.Minute)
		mon.poll(context.Background())
	}
	var crits int
	for _, a := range store.snapshot() {
		if a.level == "critical" && strings.Contains(a.msg, "行情报价") {
			crits++
		}
	}
	if crits != 1 {
		t.Fatalf("attach 后真停更应恢复 crit 分类（恰 1 条）, got alerts=%v", store.snapshot())
	}
}

// T3：attaching 持续 >15min → quote warn 恰一条含「attach」字样，零 critical。
func TestD085_AttachTimeoutWarnOnce(t *testing.T) {
	store := newFakeHealthStore()
	store.accountStatuses = map[string]string{"vA": "connected"}
	vs := &fakeVenueSource{}
	cur := wedUTC
	mon := d085Mon(store, vs, &cur)
	mon.poll(context.Background()) // attachingSince 打点
	for i := 0; i < 3; i++ {       // 16/17/18min：warn 转一次后持续同态不重复
		cur = cur.Add(16 * time.Minute / 3) // 每次推进 >15min 累计
		mon.poll(context.Background())
	}
	// 直接把时钟推过 15min 兜底再轮两次
	cur = cur.Add(20 * time.Minute)
	mon.poll(context.Background())
	cur = cur.Add(2 * time.Minute)
	mon.poll(context.Background())
	var warns, crits int
	for _, a := range store.snapshot() {
		switch {
		case a.level == "critical":
			crits++
		case a.level == "warn" && strings.Contains(a.msg, "attach"):
			warns++
		}
	}
	if crits != 0 {
		t.Fatalf("attaching 超时应零 critical, got %d: %v", crits, store.snapshot())
	}
	if warns != 1 {
		t.Fatalf("attach 超时应恰 1 条 warn, got %d: %v", warns, store.snapshot())
	}
}
