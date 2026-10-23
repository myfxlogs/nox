// bridge_sink_tier_test.go —— 分级投递锚：info 聚合成摘要桶非即时推、critical 即时径不变。
package notify

import (
	"testing"
)

func TestBridgeSink_TieredRouting(t *testing.T) {
	urlStr := "http://localhost:1" // 不达——只验入队面非投递
	s := NewAgentBridgeSink(
		func() string { return urlStr },
		func() string { return "tok" },
		func() []string { return nil },
	)
	s.Push(1, "info", "休眠轨 info 流水一件")
	if len(s.digest) != 1 || s.digest["休眠轨 info 流水一件"] != 1 {
		t.Fatalf("info 应入摘要桶非即时推: %+v", s.digest)
	}
	if len(s.jobs) != 0 {
		t.Fatalf("info 不应入即时推队列: %d", len(s.jobs))
	}
	s.Push(2, "info", "休眠轨 info 流水一件")
	if s.digest["休眠轨 info 流水一件"] != 2 {
		t.Fatalf("info 重复应计频: %+v", s.digest)
	}
	s.Push(3, "critical", "ticks stale 真实件")
	if len(s.jobs) != 1 {
		t.Fatalf("critical 应即时入队: %d", len(s.jobs))
	}
}
