// d528_wire_test.go —— D-528 S9 A6：NOX_MACRO_ENABLED 缺省=false → startMacro 直返
// 零 goroutine（生产零行为变化锚）；对照 D-513 dormant 先例。
package main

import (
	"context"
	"runtime"
	"testing"
	"time"

	"nox/internal/platform/config"
)

// D-545：env 早退闸移除=常开 goroutine（dormant 壳）；NOX_MACRO_ENABLED 热闸收敛到
// 每班 enabled() 判读（resolver 无行=false=当班跳过，当班零 fetch 锚=T4 macro 包）。
func TestD528_DisabledZeroGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startMacro(ctx, nil, config.NewSecretResolver(nil), nil) // resolver 无行=当班恒跳过
	time.Sleep(50 * time.Millisecond)
	if got := runtime.NumGoroutine(); got != before+3 { // main:macro 壳 + calendar/actuals 两 loop 常驻（D-120 清退 cot/fred）
		t.Fatalf("dormant shell goroutines: before=%d after=%d want +3", before, got)
	}
}
