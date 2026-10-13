// wire_watch.go —— 业主视窗两件套 B：盯盘引擎装配（setup.go 行数门禁拆出）。
package main

import (
	"context"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/safego"
	"nox/internal/risk/watch"
	"nox/internal/store/pgstore"
)

// wireWatchEngine 盯盘引擎——QuoteBus 通配订阅事件驱动判规则（D-004 禁轮询），
// configKick 热载（规则 CRUD 即到即生效）；stale 窗复用 DetectorFreshnessTTL。
func wireWatchEngine(ctx context.Context, configKick *bus.Topic[struct{}], st *pgstore.Store, qbus *bus.QuoteBus, staleTTL time.Duration) {
	kickCh, _ := configKick.Subscribe()
	safego.GoPlain("setup:watchEngine", func() {
		watch.New(st, qbus, staleTTL).Run(ctx, kickCh)
	})
}
