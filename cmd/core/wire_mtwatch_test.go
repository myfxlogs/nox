package main

// D-636 看门狗判定锚：①静默零火 ②单簇必报 warn ③连续 3 窗升格 critical。
import (
	"testing"
	"time"
)

func TestD636_MtWatchDecide(t *testing.T) {
	if _, _, fire := mtWatchDecide(2, 0); fire {
		t.Fatal("窗和低于阈不应告警")
	}
	level, dedup, fire := mtWatchDecide(mtWatchThresh, 0)
	if !fire || level != "warn" || dedup != 30*time.Minute {
		t.Fatalf("单簇达阈应 warn: level=%s dedup=%s fire=%v", level, dedup, fire)
	}
	level, dedup, fire = mtWatchDecide(17, 0) // SG 05:45 实测簇量
	if !fire || level != "warn" {
		t.Fatalf("实测簇量应报 warn: %s %v", level, fire)
	}
	if level, dedup, fire := mtWatchDecide(6, mtWatchEscalate); !fire || level != "critical" || dedup != 60*time.Minute {
		t.Fatalf("连续 %d 窗应升格 critical: %s %s %v", mtWatchEscalate, level, dedup, fire)
	}
}
