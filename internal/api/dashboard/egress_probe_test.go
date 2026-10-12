// egress_probe_test.go —— D-011 S1 探针聚合状态机测试。
// fake sink 隔离 dephealth 全局注册表（3 连败=degraded 镜像 FailThreshold 判据）；
// targets 换 httptest 端点，测试零外打生产三域。
package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nox/internal/infra/dephealth"
)

// fakeEgressSink per-key 连败计数（dephealth 判据最小镜像）。
type fakeEgressSink struct {
	mu   sync.Mutex
	fail map[string]int
}

func newFakeEgressSink() *fakeEgressSink { return &fakeEgressSink{fail: map[string]int{}} }

func (f *fakeEgressSink) Mark(key string, ok bool, _ error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ok {
		f.fail[key] = 0
		return
	}
	f.fail[key]++
}

func (f *fakeEgressSink) Snapshot() []dephealth.Row {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := make([]dephealth.Row, 0, len(f.fail))
	for k, n := range f.fail {
		st := dephealth.StateHealthy
		switch {
		case n == 0:
		case n >= dephealth.FailThreshold:
			st = dephealth.StateDegraded
		default:
			st = dephealth.StateSuspect
		}
		rows = append(rows, dephealth.Row{Key: k, State: st})
	}
	return rows
}

// deadURL 起一个随即关闭的服务器——其 URL 连接必被拒（传输层断线形态）。
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	return srv.URL
}

func aliveSrv(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestProbe 装配测试探针：targets 按 alias 映射到测试端点，sink/now/代理基址可控。
func newTestProbe(t *testing.T, sink *fakeEgressSink, urls map[string]string, proxyURL string, now func() time.Time) *EgressProbe {
	t.Helper()
	p := NewEgressProbe(&fakeAlertInserter{}, "")
	p.sink = sink
	p.now = now
	p.proxyURL = proxyURL
	tgs := make([]egressTarget, 0, len(egressTargets))
	for _, tg := range egressTargets {
		u, ok := urls[tg.alias]
		if !ok {
			t.Fatalf("missing test url for %s", tg.alias)
		}
		tgs = append(tgs, egressTarget{key: tg.key, url: u, alias: tg.alias})
	}
	p.targets = tgs
	return p
}

func alertsOf(f *fakeAlertInserter) []fakeAlert { return f.snapshot() }

// TestEgressProbe_AllDeadProxyAliveAggregates 双域全灭+代理可达 → 3 连败轮起恰好一条 critical。
func TestEgressProbe_AllDeadProxyAliveAggregates(t *testing.T) {
	proxy := aliveSrv(t)
	clock := time.Now()
	p := newTestProbe(t, newFakeEgressSink(),
		map[string]string{"okx": deadURL(t), "cmc": deadURL(t), "gate": deadURL(t)},
		proxy.URL, func() time.Time { return clock })
	ctx := context.Background()

	p.poll(ctx)
	p.poll(ctx) // 前两轮 suspect 未达阈
	if got := alertsOf(p.insert.(*fakeAlertInserter)); len(got) != 0 {
		t.Fatalf("alerts = %+v, want 0（未达 3 连败）", got)
	}
	p.poll(ctx)
	got := alertsOf(p.insert.(*fakeAlertInserter))
	if len(got) != 1 || got[0].level != "critical" {
		t.Fatalf("alerts = %+v, want 1 critical", got)
	}
	for _, frag := range []string{"2 个采集面", "mihomo", "okx/gate"} {
		if !strings.Contains(got[0].msg, frag) {
			t.Fatalf("msg %q missing %q", got[0].msg, frag)
		}
	}
}

// TestEgressProbe_CritRealertCooldown 全断持续 → 1h 内不重发，过窗重发一条（outage 起点不重置）。
func TestEgressProbe_CritRealertCooldown(t *testing.T) {
	proxy := aliveSrv(t)
	clock := time.Now()
	p := newTestProbe(t, newFakeEgressSink(),
		map[string]string{"okx": deadURL(t), "cmc": deadURL(t), "gate": deadURL(t)},
		proxy.URL, func() time.Time { return clock })
	ctx := context.Background()

	p.poll(ctx)
	p.poll(ctx)
	p.poll(ctx) // critical #1
	clock = clock.Add(30 * time.Minute)
	p.poll(ctx)
	if got := alertsOf(p.insert.(*fakeAlertInserter)); len(got) != 1 {
		t.Fatalf("alerts = %d, want 1（1h 冷却窗内不重发）", len(got))
	}
	clock = clock.Add(31 * time.Minute)
	p.poll(ctx)
	got := alertsOf(p.insert.(*fakeAlertInserter))
	if len(got) != 2 || got[1].level != "critical" {
		t.Fatalf("alerts = %+v, want 2 critical（过冷却窗重发）", got)
	}
}

// TestEgressProbe_RecoveryResetsCooldown 探活恢复 → 一条 info；再全断首轮即警（冷却已清）。
func TestEgressProbe_RecoveryResetsCooldown(t *testing.T) {
	proxy := aliveSrv(t)
	gate := aliveSrv(t)
	deadGate := deadURL(t)
	clock := time.Now()
	p := newTestProbe(t, newFakeEgressSink(),
		map[string]string{"okx": deadURL(t), "cmc": deadURL(t), "gate": gate.URL},
		proxy.URL, func() time.Time { return clock })
	ctx := context.Background()

	p.poll(ctx)
	p.poll(ctx)
	p.poll(ctx) // 只有一域灭（gate 存活）→ 不聚合
	if got := alertsOf(p.insert.(*fakeAlertInserter)); len(got) != 0 {
		t.Fatalf("alerts = %+v, want 0（单域存活不聚合）", got)
	}
	p.targets[1].url = deadGate // gate 也灭 → 双域全灭
	p.poll(ctx)
	p.poll(ctx) // gate 2 连败不足阈
	if got := alertsOf(p.insert.(*fakeAlertInserter)); len(got) != 0 {
		t.Fatalf("alerts = %+v, want 0（gate 未达 3 连败）", got)
	}
	p.poll(ctx) // gate 第 3 败 → 聚合 critical
	if got := alertsOf(p.insert.(*fakeAlertInserter)); len(got) != 1 {
		t.Fatalf("alerts = %+v, want 1 critical", got)
	}

	p.targets[1].url = gate.URL // gate 探活复活 → 恢复 info
	clock = clock.Add(10 * time.Minute)
	p.poll(ctx)
	got := alertsOf(p.insert.(*fakeAlertInserter))
	if len(got) != 2 || got[1].level != "info" || !strings.Contains(got[1].msg, "已恢复") {
		t.Fatalf("alerts = %+v, want 1 info 恢复", got)
	}
	p.poll(ctx) // 持续存活不重发
	if got := alertsOf(p.insert.(*fakeAlertInserter)); len(got) != 2 {
		t.Fatalf("alerts = %d, want 2（恢复态不重发）", len(got))
	}

	p.targets[1].url = deadGate // 再全断：冷却已清 → 3 连败轮首轮即警
	clock = clock.Add(5 * time.Minute)
	p.poll(ctx)
	p.poll(ctx)
	p.poll(ctx)
	got = alertsOf(p.insert.(*fakeAlertInserter))
	if len(got) != 3 || got[2].level != "critical" {
		t.Fatalf("alerts = %+v, want 第 3 条 critical（新一轮全断）", got)
	}
}

// TestEgressProbe_ProxyDeadNoAggregate 三域全灭但代理也不可达 → 归因不成立不聚合。
func TestEgressProbe_ProxyDeadNoAggregate(t *testing.T) {
	clock := time.Now()
	p := newTestProbe(t, newFakeEgressSink(),
		map[string]string{"okx": deadURL(t), "cmc": deadURL(t), "gate": deadURL(t)},
		deadURL(t), func() time.Time { return clock })
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		p.poll(ctx)
	}
	if got := alertsOf(p.insert.(*fakeAlertInserter)); len(got) != 0 {
		t.Fatalf("alerts = %+v, want 0（代理不可达不聚合）", got)
	}
}

// TestEgressProbe_NoProxyURLFailOpen BINANCE_PROXY_URL 空 → per-domain 记账照走、不聚合。
func TestEgressProbe_NoProxyURLFailOpen(t *testing.T) {
	clock := time.Now()
	p := newTestProbe(t, newFakeEgressSink(),
		map[string]string{"okx": deadURL(t), "cmc": deadURL(t), "gate": deadURL(t)},
		"", func() time.Time { return clock })
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		p.poll(ctx)
	}
	if got := alertsOf(p.insert.(*fakeAlertInserter)); len(got) != 0 {
		t.Fatalf("alerts = %+v, want 0（无代理基址不聚合）", got)
	}
}
