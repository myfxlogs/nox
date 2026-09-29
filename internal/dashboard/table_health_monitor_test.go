package dashboard

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTableHealthStore 表级新鲜度监控 fake store。
type fakeTableHealthStore struct {
	mu       sync.Mutex
	ts       map[string]time.Time // name → 最新时间戳
	err      map[string]error     // name → 注入错误（nil=正常）
	alerts   []fakeAlert
	alertErr error
}

func newFakeTableHealthStore() *fakeTableHealthStore {
	return &fakeTableHealthStore{ts: make(map[string]time.Time), err: make(map[string]error)}
}

func (s *fakeTableHealthStore) TableMaxTs(_ context.Context, name string) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.err[name]; ok {
		return time.Time{}, e
	}
	return s.ts[name], nil
}

func (s *fakeTableHealthStore) InsertAlert(_ context.Context, _ *int64, level, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.alertErr != nil {
		return s.alertErr
	}
	s.alerts = append(s.alerts, fakeAlert{level: level, msg: message})
	return nil
}

func (s *fakeTableHealthStore) setTs(name string, ts time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ts[name] = ts
}

func (s *fakeTableHealthStore) setErr(name string, e error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err[name] = e
}

func (s *fakeTableHealthStore) alertCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.alerts)
}

func (s *fakeTableHealthStore) snapshot() []fakeAlert {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]fakeAlert, len(s.alerts))
	copy(out, s.alerts)
	return out
}

// TestTableHealth_BootGraceNoAlert boot 宽限内只记状态不告警。
func TestTableHealth_BootGraceNoAlert(t *testing.T) {
	store := newFakeTableHealthStore()
	store.setTs("cmc_prices", time.Time{}) // 空表=crit
	watches := []TableWatch{{Name: "cmc_prices", WarnAfter: 10 * time.Minute, CritAfter: 30 * time.Minute}}
	mon := NewTableFreshnessMonitor(store, watches, time.Hour)
	mon.now = func() time.Time { return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC) }
	mon.bootGraceTicks = 1
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Errorf("boot grace should not alert, got %d", got)
	}
}

// TestTableHealth_StateTransitions healthy→warn→crit→recover 各转换恰一次告警/持续同态零重复。
func TestTableHealth_StateTransitions(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeTableHealthStore()
	watches := []TableWatch{{Name: "cmc_prices", WarnAfter: 10 * time.Minute, CritAfter: 30 * time.Minute}}
	mon := NewTableFreshnessMonitor(store, watches, time.Hour)
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	mon.now = func() time.Time { return base }
	mon.bootGraceTicks = 0

	// tick1: healthy (age=5m)
	store.setTs("cmc_prices", base.Add(-5*time.Minute))
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Errorf("healthy should not alert, got %d", got)
	}

	// tick2: warn (age=15m)
	store.setTs("cmc_prices", base.Add(-15*time.Minute))
	mon.poll(context.Background())
	alerts := store.snapshot()
	if len(alerts) != 1 || alerts[0].level != "warn" {
		t.Errorf("warn transition want 1 warn alert, got %v", alerts)
	}

	// tick3: crit (age=35m)
	store.setTs("cmc_prices", base.Add(-35*time.Minute))
	mon.poll(context.Background())
	alerts = store.snapshot()
	if len(alerts) != 2 || alerts[1].level != "critical" {
		t.Errorf("crit transition want 2nd critical alert, got %v", alerts)
	}

	// tick4: same crit → no repeat (within 30min realert window)
	mon.poll(context.Background())
	if got := store.alertCount(); got != 2 {
		t.Errorf("same crit should not repeat within realert window, got %d", got)
	}

	// tick5: recover (age=5m)
	store.setTs("cmc_prices", base.Add(-5*time.Minute))
	mon.poll(context.Background())
	alerts = store.snapshot()
	if len(alerts) != 3 || alerts[2].level != "info" {
		t.Errorf("recover want 1 info alert, got %v", alerts)
	}
}

// TestTableHealth_SameStateNoRepeat 同态不重复（warn 同态不重复）。
func TestTableHealth_SameStateNoRepeat(t *testing.T) {
	store := newFakeTableHealthStore()
	watches := []TableWatch{{Name: "xasset_monitor", WarnAfter: 20 * time.Minute, CritAfter: 1 * time.Hour}}
	mon := NewTableFreshnessMonitor(store, watches, time.Hour)
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	mon.now = func() time.Time { return base }
	mon.bootGraceTicks = 0

	store.setTs("xasset_monitor", base.Add(-25*time.Minute)) // warn
	mon.poll(context.Background())
	if got := store.alertCount(); got != 1 {
		t.Fatalf("first warn want 1 alert, got %d", got)
	}
	mon.poll(context.Background()) // same warn
	if got := store.alertCount(); got != 1 {
		t.Errorf("same warn should not repeat, got %d", got)
	}
}

// TestTableHealth_CritRealert crit 持续 >30min 重发一条。
func TestTableHealth_CritRealert(t *testing.T) {
	store := newFakeTableHealthStore()
	watches := []TableWatch{{Name: "cex_spread", WarnAfter: 15 * time.Minute, CritAfter: 1 * time.Hour}}
	mon := NewTableFreshnessMonitor(store, watches, time.Hour)
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	now := base
	mon.now = func() time.Time { return now }
	mon.bootGraceTicks = 0

	store.setTs("cex_spread", base.Add(-2*time.Hour)) // crit
	mon.poll(context.Background())
	if got := store.alertCount(); got != 1 {
		t.Fatalf("first crit want 1 alert, got %d", got)
	}

	// same crit within 30min → no repeat
	now = base.Add(20 * time.Minute)
	mon.poll(context.Background())
	if got := store.alertCount(); got != 1 {
		t.Errorf("crit within 30min should not repeat, got %d", got)
	}

	// crit >30min → realert
	now = base.Add(31 * time.Minute)
	mon.poll(context.Background())
	if got := store.alertCount(); got != 2 {
		t.Errorf("crit >30min should realert, got %d", got)
	}
}

// TestTableHealth_EmptyTableCrit 空表（零值时间）→critical。
func TestTableHealth_EmptyTableCrit(t *testing.T) {
	store := newFakeTableHealthStore()
	// 不 setTs → TableMaxTs 返回零值
	watches := []TableWatch{{Name: "daily_income", WarnAfter: 26 * time.Hour, CritAfter: 30 * time.Hour}}
	mon := NewTableFreshnessMonitor(store, watches, time.Hour)
	mon.now = func() time.Time { return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC) }
	mon.bootGraceTicks = 0
	mon.poll(context.Background())
	alerts := store.snapshot()
	if len(alerts) != 1 || alerts[0].level != "critical" {
		t.Errorf("empty table want 1 critical alert, got %v", alerts)
	}
}

// TestTableHealth_RecoveredOneAlert recovered 恰一次告警。
func TestTableHealth_RecoveredOneAlert(t *testing.T) {
	store := newFakeTableHealthStore()
	watches := []TableWatch{{Name: "trx_account", WarnAfter: 15 * time.Minute, CritAfter: 1 * time.Hour}}
	mon := NewTableFreshnessMonitor(store, watches, time.Hour)
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	mon.now = func() time.Time { return base }
	mon.bootGraceTicks = 0

	store.setTs("trx_account", base.Add(-2*time.Hour)) // crit
	mon.poll(context.Background())
	store.setTs("trx_account", base.Add(-5*time.Minute)) // healthy
	mon.poll(context.Background())
	store.setTs("trx_account", base.Add(-5*time.Minute)) // same healthy
	mon.poll(context.Background())
	alerts := store.snapshot()
	if len(alerts) != 2 {
		t.Fatalf("want 2 alerts (crit+recover), got %d", len(alerts))
	}
	if alerts[1].level != "info" {
		t.Errorf("recover want info, got %s", alerts[1].level)
	}
	if !strings.Contains(alerts[1].msg, "已恢复") {
		t.Errorf("recover msg should contain 'recovered', got %s", alerts[1].msg)
	}
}
