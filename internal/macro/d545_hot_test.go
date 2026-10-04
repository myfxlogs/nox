package macro

// D-545 T4 验收锚：ARB_MACRO_ENABLED 热闸——Enabled()=false 当班零 fetch；
// FredKey 热更→下一班 fetch 携新值（fake getter 断言返回值）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// d545Store 最小 Store 实现（T4 只消费 UpsertFredObs，其余兜底空返回）。
type d545Store struct{ fredRows atomic.Int32 }

func (s *d545Store) UpsertMacroEvents(context.Context, []pgstore.MacroEvent) (int, error) {
	return 0, nil
}
func (s *d545Store) UpsertCotPositions(context.Context, []pgstore.CotPosition) (int, error) {
	return 0, nil
}
func (s *d545Store) UpsertFredObs(_ context.Context, rows []pgstore.FredObs) (int, error) {
	s.fredRows.Add(int32(len(rows)))
	return len(rows), nil
}
func (s *d545Store) MacroEventsBetween(context.Context, time.Time, time.Time) ([]pgstore.MacroEvent, error) {
	return nil, nil
}
func (s *d545Store) MaxCotReportDate(context.Context) (time.Time, error) {
	return time.Time{}, nil
}
func (s *d545Store) InsertAlertDedup(context.Context, string, string, time.Duration) error {
	return nil
}
func (s *d545Store) NextHighEventAt(context.Context, time.Time) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (s *d545Store) MacroHighEventsIn(context.Context, time.Time, time.Time, []string) ([]pgstore.MacroEvent, error) {
	return nil, nil
}

func TestD545_T4_EnabledGateAndHotFredKey(t *testing.T) {
	var hits atomic.Int32
	var lastKey atomic.Value
	lastKey.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		lastKey.Store(r.URL.Query().Get("api_key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"observations":[{"date":"2026-09-28","value":"4.50"}]}`))
	}))
	defer srv.Close()
	origURL, origSleep := fredURL, sleepUntil
	fredURL = srv.URL

	var wakes atomic.Int32
	var released atomic.Bool
	sleepUntil = func(ctx context.Context, _ time.Time) bool {
		wakes.Add(1)
		for {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(5 * time.Millisecond):
			}
			if released.Load() {
				return true
			}
		}
	}

	var enabled atomic.Bool
	var key atomic.Value
	key.Store("")
	d := Deps{
		Store:   &d545Store{},
		FredKey: func() string { return key.Load().(string) },
		Enabled: func() bool { return enabled.Load() },
		Now:     func() time.Time { return time.Now() },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { fredLoop(ctx, d, srv.Client()); close(done) }()
	defer func() { // 先停 loop 再还原注入点（防 var 读写竞态）。
		cancel()
		<-done
		fredURL, sleepUntil = origURL, origSleep
	}()

	// T4① enabled=false：先放行一班——当班应零 fetch。
	released.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for wakes.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("enabled=false 当班应零 fetch, got %d hits", got)
	}
	if got := wakes.Load(); got < 1 {
		t.Fatal("sleepUntil 未被调用——loop 未进当班")
	}

	// T4② enabled 翻 true + FredKey 热值——下一班 fetch 携新 key。
	key.Store("hot-fred-key-2")
	enabled.Store(true)
	deadline = time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hits.Load() == 0 {
		t.Fatal("enabled=true 当班应有 fetch")
	}
	if got := lastKey.Load().(string); got != "hot-fred-key-2" {
		t.Fatalf("fetch 未携热值: got api_key=%q", got)
	}
}
