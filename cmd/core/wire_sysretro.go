// wire_sysretro.go —— D-457 S3：系统周期复盘报告装配。
// 双定时器（D-510 切北京历日）：日报每日 00:30 北京时间、周报每周一 00:30 北京时间；env NOX_RETRO_ENABLED=0 可关。
// AI client 走 kbreport.ResolveClient（DB 优先 env 回落）；独立限流不占前端配额。
package main

import (
	"context"
	"log"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/review/kbreport"
	"nox/internal/review/sysretro"
	"nox/internal/store/pgstore"
)

// startRetroReporter 装配并启动复盘报告 goroutine（env 关则不起）。
func startRetroReporter(ctx context.Context, st *pgstore.Store, cfg *config.Config) {
	if cfg.RetroDisabled {
		log.Printf("[sysretro] disabled by NOX_RETRO_ENABLED")
		return
	}
	tools := &analyst.Tools{ReadOnlyDSN: cfg.AIPGDSN, Store: st, KnRoot: analyst.KnRoot}
	safego.GoLoop("main:sysretroDaily", func() { retroLoop(ctx, st, cfg, tools, "daily") })
	safego.GoLoop("main:sysretroWeekly", func() { retroLoop(ctx, st, cfg, tools, "weekly") })
}

// retroDeps 装配 Generate 依赖（store+工具+默认路径全在包内 fill）。
func retroDeps(st *pgstore.Store, tools analyst.ToolExecutor) sysretro.Deps {
	return sysretro.Deps{Store: st, Tools: tools}
}

// retroLoop 定时循环：对齐下一触发时刻 → Generate → InsertAlert 完成通知（err→log warn 不炸）。
func retroLoop(ctx context.Context, st *pgstore.Store, cfg *config.Config, tools analyst.ToolExecutor, kind string) {
	next := sysretro.NextDelay(time.Now(), kind)
	timer := time.NewTimer(next)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		client, err := kbreport.ResolveClient(ctx, st, cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
		if err != nil {
			log.Printf("[sysretro] resolve client: %v", err)
		} else {
			deps := retroDeps(st, tools)
			deps.Client = client
			var path string
			if kind == "daily" {
				path, err = sysretro.GenerateDaily(ctx, deps)
			} else {
				path, err = sysretro.GenerateWeekly(ctx, deps)
			}
			if err != nil {
				log.Printf("[sysretro] generate %s: %v", kind, err)
			} else {
				log.Printf("[sysretro] %s report: %s", kind, path)
			}
		}
		timer.Reset(sysretro.NextDelay(time.Now(), kind))
	}
}
