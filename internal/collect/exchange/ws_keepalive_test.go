// ws_keepalive_test.go —— D-507：reconnectMeter 滑窗/阈值/rewarn 单测（假时钟纯逻辑）。
package exchange

import (
	"sync"
	"testing"
	"time"
)

func TestD507_ReconnectMeterWindow(t *testing.T) {
	var mu sync.Mutex
	var warns []string
	m := newReconnectMeter("wss://x/ws", func(level, msg string) {
		mu.Lock()
		warns = append(warns, level+":"+msg)
		mu.Unlock()
	})
	base := time.Unix(1790400000, 0)
	m.now = func() time.Time { return base }
	nWarns := func() int { mu.Lock(); defer mu.Unlock(); return len(warns) }

	for i := 0; i < 5; i++ { // 窗内 5 次未达阈 → 零告警
		m.record()
	}
	if n := nWarns(); n != 0 {
		t.Fatalf("5 次重连即告警: %v", warns)
	}
	m.record() // 第 6 次达阈 → warn×1
	if n := nWarns(); n != 1 {
		t.Fatalf("6 次应告警一次, got %d", n)
	}
	m.record() // 窗内再 record 不重复告警
	if n := nWarns(); n != 1 {
		t.Fatalf("rewarn 窗内重复告警: %d", n)
	}
	// 时钟推 2h+（rewarn 间隔过）+ 再满阈 → 第二条
	base = base.Add(reconnectMeterRewarn + time.Hour)
	for i := 0; i < reconnectMeterThreshold; i++ {
		m.record()
	}
	if n := nWarns(); n != 2 {
		t.Fatalf("rewarn 间隔后应第二条, got %d", n)
	}
}
