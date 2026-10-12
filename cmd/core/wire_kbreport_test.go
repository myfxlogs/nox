package main

import (
	"testing"
	"time"
)

// TestDelayUntilNextMondayUTC D-442 S3：周一 00:00 UTC 对齐边界。
func TestDelayUntilNextMondayUTC(t *testing.T) {
	// 周三 → 下周一
	d := delayUntilNextMondayUTC(time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)) // 周三
	if d != 5*24*time.Hour-12*time.Hour {
		t.Errorf("wed→mon delay=%v", d)
	}
	// 周一 00:00 整 → 立即（0）
	d = delayUntilNextMondayUTC(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC))
	if d != 0 {
		t.Errorf("mon 00:00 delay=%v want 0", d)
	}
	// 周一 00:01 → 下周
	d = delayUntilNextMondayUTC(time.Date(2026, 9, 21, 0, 1, 0, 0, time.UTC))
	if d != 7*24*time.Hour-time.Minute {
		t.Errorf("mon 00:01 delay=%v", d)
	}
}
