// d157_watchdog_test.go —— D-157：trx_apr stale 阈值随 1h failsafe 兜底口径。
// 阈须 ≥2×failsafe 周期（容忍一轮失败重试窗）且 <边界周期 6h（真死可检）。
package trx

import (
	"testing"
	"time"
)

func TestD157_AprStaleThreshold(t *testing.T) {
	const failsafe = time.Hour // wire_trx.go trx_apr Interval（双写锚——改一边另一边须同步）
	if aprStaleThreshold < 2*failsafe {
		t.Fatalf("stale 阈 %v < 2×failsafe %v——一轮失败即误报（D-157 修复对象）", aprStaleThreshold, failsafe)
	}
	if aprStaleThreshold > 6*time.Hour {
		t.Fatalf("stale 阈 %v > 边界周期 6h——真死检出超票面验收 ≤3h 上限", aprStaleThreshold)
	}
}
