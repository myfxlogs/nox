// d513_wire_test.go —— D-513 S6 A5：NOX_EXPLORER_ENABLED 缺省=false → startExplorer 直返
// 零 goroutine（生产零行为变化锚）；置位=true 才起 goroutine。
package main

import (
	"context"
	"runtime"
	"testing"
	"time"

	"nox/internal/platform/config"
)

func TestD513_DisabledZeroGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := &config.Config{ExplorerEnabled: false} // env 缺省=false 同形
	startExplorer(ctx, nil, cfg, nil)
	time.Sleep(50 * time.Millisecond)
	if got := runtime.NumGoroutine(); got > before {
		t.Fatalf("disabled explorer spawned goroutine: before=%d after=%d", before, got)
	}
}
