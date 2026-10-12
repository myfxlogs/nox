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
		m.record(false)
	}
	if n := nWarns(); n != 0 {
		t.Fatalf("5 次重连即告警: %v", warns)
	}
	m.record(false) // 第 6 次达阈 → warn×1
	if n := nWarns(); n != 1 {
		t.Fatalf("6 次应告警一次, got %d", n)
	}
	m.record(false) // 窗内再 record 不重复告警
	if n := nWarns(); n != 1 {
		t.Fatalf("rewarn 窗内重复告警: %d", n)
	}
	// 时钟推 2h+（rewarn 间隔过）+ 再满阈 → 第二条
	base = base.Add(reconnectMeterRewarn + time.Hour)
	for i := 0; i < reconnectMeterThreshold; i++ {
		m.record(false)
	}
	if n := nWarns(); n != 2 {
		t.Fatalf("rewarn 间隔后应第二条, got %d", n)
	}
}

// TestD074_CooldownLoopStormExclusion D-074 ②：冷却循环中的重连（inCooldownLoop=
// true）不入 storm 滑窗（超阈零告警），累计计数照常；false 重连恢复计风暴。
func TestD074_CooldownLoopStormExclusion(t *testing.T) {
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

	for i := 0; i < 20; i++ { // 冷却循环中 20 连杀——远超风暴阈仍零告警
		m.record(true)
	}
	if n := nWarns(); n != 0 {
		t.Fatalf("冷却循环重连不应计风暴: %v", warns)
	}
	if got := m.totalReconns(); got != 20 {
		t.Fatalf("累计计数应照常: %d want 20", got)
	}
	base = base.Add(2 * time.Hour)
	for i := 0; i < reconnectMeterThreshold; i++ { // 冷却循环解除 → 恢复风暴判定
		m.record(false)
	}
	if n := nWarns(); n != 1 {
		t.Fatalf("循环解除后满阈应告警, got %d", n)
	}
}
