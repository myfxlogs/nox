// D-337R2 S1：server 时区 fetcher 适配（原 wireServerTZ 收敛——tzCache 已改 NewBrokerPositionMonitor
// 构造必选注入，忘接=编译失败；setter 已删。fetcher 每次调用经 BrokerManager.ServerTZQueriers
// 现取，跟随 attach/detach）。
package main

import (
	"context"

	"nox/internal/api/dashboard"
)

func serverTZFetcherFor(bm *dashboard.BrokerManager) dashboard.ServerTZFetcher {
	return func(ctx context.Context, venueKey string) (float64, error) {
		q, ok := bm.ServerTZQueriers()[venueKey]
		if !ok {
			return 0, context.Canceled // 非 MT5/未 attach → 本轮诚实空态
		}
		return q.ServerTimezone(ctx)
	}
}
