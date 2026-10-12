package dephealth

import (
	"errors"
	"testing"
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
