package dashboard

import (
	"context"
	"sync"
)

// fakeAlertInserter D-252：告警 fake store（线程安全，race 测试用）。
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
