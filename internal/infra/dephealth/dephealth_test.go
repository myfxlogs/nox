package dephealth

import (
	"errors"
	"testing"
	"time"
)

// TestD109_DegradedReader D-109：单键 degraded 读口——未记录键 false、连败达阈 true、
// 恢复后 false（REST 死流兜底看门狗闸的读面锚）。
func TestD109_DegradedReader(t *testing.T) {
	resetForTest()
	defer resetForTest()
	Init(nil)
	if Degraded("binance:ws-perp-markPrice") {
		t.Fatal("未记录键应 false")
	}
	for i := 0; i < FailThreshold; i++ {
		Mark("binance:ws-perp-markPrice", false, errors.New("dial tcp: refused"))
	}
	if !Degraded("binance:ws-perp-markPrice") {
		t.Fatal("连败达阈应 degraded")
	}
	Mark("binance:ws-perp-markPrice", true, nil)
	if Degraded("binance:ws-perp-markPrice") {
		t.Fatal("恢复后应 false")
	}
}

// TestMarkFrameOK_ThrottleAndAdvance bn 解禁判据锚：帧到喂入 lastEventAt 前移
// 可证（两样本不等）+节流 ≤1/min/键防帧频刷表。
func TestMarkFrameOK_ThrottleAndAdvance(t *testing.T) {
	mu.Lock()
	delete(reg, "binance:ws-perp-markPrice")
	delete(frameMarkAt, "binance:ws-perp-markPrice")
	mu.Unlock()

	base := time.Now()
	nowFn = func() time.Time { return base }
	defer func() { nowFn = time.Now }()

	MarkFrameOK("binance:ws-perp-markPrice")
	mu.Lock()
	e := reg["binance:ws-perp-markPrice"]
	first := e.lastEventAt
	mu.Unlock()
	if first.IsZero() {
		t.Fatal("MarkFrameOK 首喂后 lastEventAt 零")
	}

	// 30s 后同键再喂——节流窗内不更新（防帧频刷表）
	nowFn = func() time.Time { return base.Add(30 * time.Second) }
	MarkFrameOK("binance:ws-perp-markPrice")
	mu.Lock()
	if reg["binance:ws-perp-markPrice"].lastEventAt != first {
		t.Fatal("节流窗内 lastEventAt 被刷")
	}
	mu.Unlock()

	// 61s 后帧到——节流窗过=前移（解禁判据「两样本不等」达标）
	nowFn = func() time.Time { return base.Add(61 * time.Second) }
	MarkFrameOK("binance:ws-perp-markPrice")
	mu.Lock()
	got := reg["binance:ws-perp-markPrice"].lastEventAt
	mu.Unlock()
	if got == first || got.Before(first) {
		t.Fatalf("节流窗后 lastEventAt 未前移: %v vs %v", got, first)
	}
}
