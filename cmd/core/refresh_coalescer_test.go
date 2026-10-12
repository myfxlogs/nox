package main

import (
	"testing"
	"time"

	"nox/internal/data/bus"
)

// TestRefreshCoalescer_Coalesces 热修 hotfix-paper：同 key 窗内 N 次 → 补发 1 次（0.5/s 上界）。
func TestRefreshCoalescer_Coalesces(t *testing.T) {
	old := refreshCoalesceWindow
	refreshCoalesceWindow = 30 * time.Millisecond
	t.Cleanup(func() { refreshCoalesceWindow = old })
	topic := bus.NewTopic[string]()
	ch, cancel := topic.Subscribe()
	defer cancel()
	c := newRefreshCoalescer(topic)
	for i := 0; i < 5; i++ {
		c.Publish("paper_carry")
	}
	// topic buffered(1) 丢旧保新：逐窗先消费再断言。
	time.Sleep(60 * time.Millisecond)
	select {
	case k := <-ch:
		if k != "paper_carry" {
			t.Fatalf("key=%s, want paper_carry", k)
		}
	default:
		t.Fatal("paper_carry: no publish after window（5 次调用应合流为 1）")
	}
	select {
	case k := <-ch:
		t.Fatalf("extra publish %s（合流应只发 1 次）", k)
	default:
	}
	c.Publish("carry_graduation") // 异 key 各自挂补发
	time.Sleep(60 * time.Millisecond)
	select {
	case k := <-ch:
		if k != "carry_graduation" {
			t.Fatalf("key=%s, want carry_graduation", k)
		}
	default:
		t.Fatal("carry_graduation: no publish after window")
	}
}
