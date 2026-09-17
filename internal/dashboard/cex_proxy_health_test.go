// CexProxyHealthMonitor 单测（D-301b P1-P4）：httptest 端点可控成败，直接驱动 poll。
package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newProxyTestMonitor 测试构造：直填字段绕过 env（env 路径由 P4 覆盖）。
func newProxyTestMonitor(t *testing.T, insert alertInserter, primary, backup string) *CexProxyHealthMonitor {
	t.Helper()
	return &CexProxyHealthMonitor{
		insert:    insert,
		client:    &http.Client{Timeout: cexProxyPollTimeout},
		primary:   primary,
		backup:    backup,
		tick:      cexProxyTick,
		consec:    make(map[string]int),
		deadNoted: make(map[string]bool),
		lastOK:    make(map[string]bool),
	}
}

// statusServer 可控成败的假代理端点（ok=false → 502）。
func statusServer(t *testing.T, ok *bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if ok == nil || *ok {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
}

// deadServer 已关闭的服务器 URL（连接拒绝=dead）。
func deadServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Close()
	return srv.URL
}

// TestCexProxyHealth_CritOnceThenRecover P1+P2：连续 3 败→critical 一条（同态不重发）；
// 恢复→info recovered 一条；恢复后持续健康不再发。
func TestCexProxyHealth_CritOnceThenRecover(t *testing.T) {
	ok := false
	srv := statusServer(t, &ok)
	defer srv.Close()
	fake := &fakeAlertInserter{}
	m := newProxyTestMonitor(t, fake, srv.URL, "")

	m.poll(context.Background()) // 1 败
	m.poll(context.Background()) // 2 败：未达阈值
	if n := len(fake.snapshot()); n != 0 {
		t.Fatalf("P1: before threshold want 0 alerts, got %d", n)
	}
	m.poll(context.Background()) // 3 败：critical 一条
	alerts := fake.snapshot()
	if len(alerts) != 1 || alerts[0].level != "critical" {
		t.Fatalf("P1: want 1 critical at 3rd fail, got %+v", alerts)
	}
	if !strings.Contains(alerts[0].msg, srv.URL) || !strings.Contains(alerts[0].msg, "unreachable (3 consecutive)") {
		t.Errorf("P1: msg want 'cex-proxy <ep> unreachable (3 consecutive)' fingerprint, got %q", alerts[0].msg)
	}
	m.poll(context.Background())
	m.poll(context.Background())
	if n := len(fake.snapshot()); n != 1 {
		t.Fatalf("P1: same-state want no repeat, got %d alerts", n)
	}

	ok = true
	m.poll(context.Background()) // 恢复
	alerts = fake.snapshot()
	if len(alerts) != 2 || alerts[1].level != "info" || !strings.Contains(alerts[1].msg, "recovered") {
		t.Fatalf("P2: want info recovered as 2nd alert, got %+v", alerts)
	}
	m.poll(context.Background()) // 持续健康
	if n := len(fake.snapshot()); n != 2 {
		t.Errorf("P2: steady-state healthy want no new alerts, got %d", n)
	}
}

// TestCexProxyHealth_BackupAliveSwitchSignal P3：primary dead+backup alive → primary 的
// critical 文案含 backup=<ep> alive（可切换信号）；backup 自身健康不告警。
func TestCexProxyHealth_BackupAliveSwitchSignal(t *testing.T) {
	alive := statusServer(t, nil)
	defer alive.Close()
	dead := deadServer(t)
	fake := &fakeAlertInserter{}
	m := newProxyTestMonitor(t, fake, dead, alive.URL)

	for i := 0; i < 3; i++ {
		m.poll(context.Background())
	}
	alerts := fake.snapshot()
	if len(alerts) != 1 || alerts[0].level != "critical" {
		t.Fatalf("P3: want 1 critical (primary only), got %+v", alerts)
	}
	if !strings.Contains(alerts[0].msg, "backup="+alive.URL+" alive") {
		t.Errorf("P3: primary alert want switch signal 'backup=<ep> alive', got %q", alerts[0].msg)
	}
}

// TestCexProxyHealth_BothDeadDistinguished P3 补：两端皆 dead → 两条 critical，各自文案
// 显式带对端 dead（与「backup alive 可切换」区分，全盲信号）。
func TestCexProxyHealth_BothDeadDistinguished(t *testing.T) {
	dead1 := deadServer(t)
	dead2 := deadServer(t)
	fake := &fakeAlertInserter{}
	m := newProxyTestMonitor(t, fake, dead1, dead2)

	for i := 0; i < 3; i++ {
		m.poll(context.Background())
	}
	alerts := fake.snapshot()
	if len(alerts) != 2 {
		t.Fatalf("both-dead: want 2 criticals (primary+backup), got %+v", alerts)
	}
	if !strings.Contains(alerts[0].msg, "backup="+dead2+" dead") {
		t.Errorf("both-dead: primary alert want 'backup=<ep> dead', got %q", alerts[0].msg)
	}
	if !strings.Contains(alerts[1].msg, "primary="+dead1+" dead") {
		t.Errorf("both-dead: backup alert want 'primary=<ep> dead', got %q", alerts[1].msg)
	}
}

// TestCexProxyHealth_EmptyEnvFailOpen P4：BINANCE_PROXY_URL 空 → 构造返回 nil（不监控不崩，
// fail-open）；配置 primary（backup 空）→ 单端监控。
func TestCexProxyHealth_EmptyEnvFailOpen(t *testing.T) {
	t.Setenv("BINANCE_PROXY_URL", "")
	if m := NewCexProxyHealthMonitor(&fakeAlertInserter{}); m != nil {
		t.Fatal("P4: empty BINANCE_PROXY_URL want nil monitor (fail-open)")
	}
	t.Setenv("BINANCE_PROXY_URL", "http://example.invalid")
	t.Setenv("BINANCE_PROXY_URL_BACKUP", "")
	m := NewCexProxyHealthMonitor(&fakeAlertInserter{})
	if m == nil {
		t.Fatal("P4: primary set want monitor")
	}
	if m.backup != "" {
		t.Errorf("P4: backup want empty, got %q", m.backup)
	}
	if eps := m.endpoints(); len(eps) != 1 || eps[0] != "http://example.invalid" {
		t.Errorf("P4: endpoints want single primary, got %v", eps)
	}
}
