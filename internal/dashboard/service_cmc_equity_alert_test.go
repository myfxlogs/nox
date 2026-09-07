// D-252 CMC 净值阈值告警状态机测试（先红后绿）。
// ⑴ normal→warning：2 tick 去抖后恰 1 条 critical，持续 warning 再 10 tick 零新增
// ⑵ warning→normal：恰 1 条 info recovered；单 tick 抖动（normal→warning→normal 各 1 tick）→ 零告警
// ⑶ warning→closeout 升级：恰 1 条 critical（文本含"强平线"）
// ⑷ 受影响包 race 绿
package dashboard

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeAlertInserter D-252：净值告警 fake alert store（线程安全，race 测试用）。
type fakeAlertInserter struct {
	mu     sync.Mutex
	alerts []fakeAlert
}

func (f *fakeAlertInserter) InsertAlert(_ context.Context, _ *int64, level, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, fakeAlert{level: level, msg: message})
	return nil
}

func (f *fakeAlertInserter) snapshot() []fakeAlert {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeAlert, len(f.alerts))
	copy(out, f.alerts)
	return out
}

func (f *fakeAlertInserter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.alerts)
}

// TestD252_NormalToWarning D-252 ⑴：normal→warning 2 tick 去抖后恰 1 条 critical，
// 持续 warning 再 10 tick 零新增。
func TestD252_NormalToWarning(t *testing.T) {
	store := &fakeAlertInserter{}
	m := newCMCEquityAlertMachine(store)
	// tick1 (firstTick): confirmed=normal, no alert
	m.check(context.Background(), 1000, 800, 500) // equity=1000 > warning=800 → normal
	if got := store.count(); got != 0 {
		t.Fatalf("tick1 (firstTick): want 0 alerts, got %d", got)
	}
	// tick2: observe warning, pending=warning, count=1 → no alert
	m.check(context.Background(), 700, 800, 500) // equity=700 ≤ warning=800 → warning
	if got := store.count(); got != 0 {
		t.Fatalf("tick2 (debounce count=1): want 0 alerts, got %d", got)
	}
	// tick3: observe warning again, count=2 → transition, 1 critical
	m.check(context.Background(), 700, 800, 500)
	if got := store.count(); got != 1 {
		t.Fatalf("tick3 (debounce count=2 → transition): want 1 alert, got %d", got)
	}
	alerts := store.snapshot()
	if alerts[0].level != "critical" {
		t.Errorf("tick3: want critical, got %s", alerts[0].level)
	}
	if !strings.Contains(alerts[0].msg, "预警线") {
		t.Errorf("tick3: msg should contain 预警线, got %s", alerts[0].msg)
	}
	// tick4-13: sustained warning, 10 ticks zero new
	for i := 0; i < 10; i++ {
		m.check(context.Background(), 700, 800, 500)
	}
	if got := store.count(); got != 1 {
		t.Fatalf("sustained warning 10 ticks: want 1 total alert, got %d", got)
	}
}

// TestD252_WarningToNormal D-252 ⑵：warning→normal 恰 1 条 info recovered；
// 单 tick 抖动（normal→warning→normal 各 1 tick）→ 零告警（去抖实证）。
func TestD252_WarningToNormal(t *testing.T) {
	store := &fakeAlertInserter{}
	m := newCMCEquityAlertMachine(store)
	// Establish warning state: firstTick normal + 2 ticks warning
	m.check(context.Background(), 1000, 800, 500) // tick1 firstTick: confirmed=normal
	m.check(context.Background(), 700, 800, 500)  // tick2: pending=warning count=1
	m.check(context.Background(), 700, 800, 500)  // tick3: count=2 → transition to warning, 1 critical
	if got := store.count(); got != 1 {
		t.Fatalf("setup (enter warning): want 1 alert, got %d", got)
	}
	// warning→normal: 2 ticks to exit
	m.check(context.Background(), 1000, 800, 500) // tick: observe normal, pending=normal count=1
	if got := store.count(); got != 1 {
		t.Fatalf("warning→normal tick1 (debounce): want 1, got %d", got)
	}
	m.check(context.Background(), 1000, 800, 500) // tick: count=2 → transition to normal, 1 info
	if got := store.count(); got != 2 {
		t.Fatalf("warning→normal tick2 (transition): want 2, got %d", got)
	}
	alerts := store.snapshot()
	if alerts[1].level != "info" {
		t.Errorf("recover: want info, got %s", alerts[1].level)
	}
	if !strings.Contains(alerts[1].msg, "recovered") {
		t.Errorf("recover: msg should contain recovered, got %s", alerts[1].msg)
	}
}

// TestD252_SingleTickJitterZeroAlerts D-252 ⑵b：单 tick 抖动（normal→warning→normal 各 1 tick）→ 零告警。
func TestD252_SingleTickJitterZeroAlerts(t *testing.T) {
	store := &fakeAlertInserter{}
	m := newCMCEquityAlertMachine(store)
	// tick1 (firstTick): confirmed=normal
	m.check(context.Background(), 1000, 800, 500)
	// tick2: observe warning (pending=warning, count=1)
	m.check(context.Background(), 700, 800, 500)
	// tick3: observe normal (pending was warning != normal → reset pending=normal, count=1)
	m.check(context.Background(), 1000, 800, 500)
	if got := store.count(); got != 0 {
		t.Fatalf("single tick jitter: want 0 alerts, got %d (alerts=%v)", got, store.snapshot())
	}
}

// TestD252_WarningToCloseout D-252 ⑶：warning→closeout 升级，恰 1 条 critical（文本含"强平线"）。
func TestD252_WarningToCloseout(t *testing.T) {
	store := &fakeAlertInserter{}
	m := newCMCEquityAlertMachine(store)
	// Establish warning state: firstTick normal + 2 ticks warning
	m.check(context.Background(), 1000, 800, 500) // firstTick: confirmed=normal
	m.check(context.Background(), 700, 800, 500)  // pending count=1
	m.check(context.Background(), 700, 800, 500)  // count=2 → transition to warning, 1 critical
	if got := store.count(); got != 1 {
		t.Fatalf("setup (enter warning): want 1 alert, got %d", got)
	}
	// warning→closeout: 2 ticks to upgrade
	m.check(context.Background(), 400, 800, 500) // observe closeout (equity=400 ≤ closeout=500), count=1
	if got := store.count(); got != 1 {
		t.Fatalf("closeout tick1 (debounce): want 1, got %d", got)
	}
	m.check(context.Background(), 400, 800, 500) // count=2 → transition to closeout, 1 critical
	if got := store.count(); got != 2 {
		t.Fatalf("closeout tick2 (transition): want 2, got %d", got)
	}
	alerts := store.snapshot()
	if alerts[1].level != "critical" {
		t.Errorf("closeout: want critical, got %s", alerts[1].level)
	}
	if !strings.Contains(alerts[1].msg, "强平线") {
		t.Errorf("closeout: msg should contain 强平线, got %s", alerts[1].msg)
	}
}

// TestD252_RaceConcurrentChecks D-252 ⑷：并发 check 调用不 panic（race 绿）。
func TestD252_RaceConcurrentChecks(t *testing.T) {
	store := &fakeAlertInserter{}
	m := newCMCEquityAlertMachine(store)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			equity := 1000.0
			if n%3 == 0 {
				equity = 700 // warning
			} else if n%3 == 1 {
				equity = 400 // closeout
			}
			m.check(context.Background(), equity, 800, 500)
		}(i)
	}
	wg.Wait()
}

// TestD252_RH1_MainWiring D-252 R-H1：cmd/ 恰 1 命中 SetCMCEquityAlertStore（main 装配实证）。
func TestD252_RH1_MainWiring(t *testing.T) {
	_, testFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(testFile), "..", "..")
	cmdDir := filepath.Join(repoRoot, "cmd")
	var hits []string
	err := filepath.Walk(cmdDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(data), "SetCMCEquityAlertStore") {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk cmd/: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("SetCMCEquityAlertStore in cmd/: got %d hits %v, want exactly 1 (R-H1 main 装配)", len(hits), hits)
	}
}

// TestD252_RH2_AlertTextContainsValues D-252 R-H2：告警文本含实际 equity/线值。
func TestD252_RH2_AlertTextContainsValues(t *testing.T) {
	store := &fakeAlertInserter{}
	m := newCMCEquityAlertMachine(store)
	// normal → warning：文本含 equity 值与 warning 线值
	m.check(context.Background(), 1000, 800, 500) // firstTick normal
	m.check(context.Background(), 700, 800, 500)  // pending warning count=1
	m.check(context.Background(), 700, 800, 500)  // count=2 → transition, 1 critical
	alerts := store.snapshot()
	if len(alerts) != 1 {
		t.Fatalf("want 1 alert, got %d", len(alerts))
	}
	if !strings.Contains(alerts[0].msg, "700") {
		t.Errorf("warning alert should contain equity value 700, got %q", alerts[0].msg)
	}
	if !strings.Contains(alerts[0].msg, "800") {
		t.Errorf("warning alert should contain warning level 800, got %q", alerts[0].msg)
	}

	// warning → closeout：文本含 equity 值与 closeout 线值
	m.check(context.Background(), 400, 800, 500) // pending closeout count=1
	m.check(context.Background(), 400, 800, 500) // count=2 → transition, 1 critical
	alerts = store.snapshot()
	if len(alerts) != 2 {
		t.Fatalf("want 2 alerts, got %d", len(alerts))
	}
	if !strings.Contains(alerts[1].msg, "400") {
		t.Errorf("closeout alert should contain equity value 400, got %q", alerts[1].msg)
	}
	if !strings.Contains(alerts[1].msg, "500") {
		t.Errorf("closeout alert should contain closeout level 500, got %q", alerts[1].msg)
	}

	// closeout → normal：recovered 文本含 equity 值
	m.check(context.Background(), 1000, 800, 500) // pending normal count=1
	m.check(context.Background(), 1000, 800, 500) // count=2 → transition, 1 info
	alerts = store.snapshot()
	if len(alerts) != 3 {
		t.Fatalf("want 3 alerts, got %d", len(alerts))
	}
	if !strings.Contains(alerts[2].msg, "1000") {
		t.Errorf("recovered alert should contain equity value 1000, got %q", alerts[2].msg)
	}
}
