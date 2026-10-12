// wire_explorer.go —— D-513 S5：探矿员壳装配（env 总闸 NOX_EXPLORER_ENABLED 缺省 false
// 直接 return=零 goroutine 零行为变化；独立令牌桶不占前端 analyst 配额）。
// AI client 走 kbreport.ResolveClient（DB 优先 env 回落，同 sysretro）。
package main

import (
	"context"
	"log"

	"nox/internal/data/bus"
	"nox/internal/platform/analyst"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/research/explorer"
	"nox/internal/review/kbreport"
	"nox/internal/store/pgstore"
)

// startExplorer 装配并启动探矿员 goroutine（env 闸缺省关→直接 return 零 goroutine）。
// probeTopic D-163：提案落库 → 机审 kick（nil=不踢）。
func startExplorer(ctx context.Context, st *pgstore.Store, cfg *config.Config, probeTopic *bus.Topic[struct{}]) {
	if !cfg.ExplorerEnabled {
		return
	}
	tools := &analyst.Tools{ReadOnlyDSN: cfg.AIPGDSN, Store: st, KnRoot: analyst.KnRoot}
	deps := explorer.Deps{
		Store: st,
		Tools: tools,
		Rate:  analyst.NewRateLimiter(cfg.ExplorerRatePerHour),
		ProbeKick: func() {
			if probeTopic != nil {
				probeTopic.Publish(struct{}{})
			}
		},
		ResolveClient: func(ctx context.Context) (*analyst.Client, error) {
			return kbreport.ResolveClient(ctx, st, cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
		},
	}
	safego.GoLoop("main:explorer", func() { explorer.Run(ctx, deps, nil) })
	log.Printf("[explorer] enabled: daily 06:47 BJ + weekly sun 07:13 BJ")
}
