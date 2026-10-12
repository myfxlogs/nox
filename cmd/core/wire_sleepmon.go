// wire_sleepmon.go —— D-534 B4：睡钱触发层装配（BJ 历日每 30min tick 查今日未跑）。
// env 闸 NOX_SLEEPMON_ENABLED=0/false 关，缺省 true（只写自表+alert+inbox，无外部副作用）。
package main

import (
	"context"
	"log"

	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/portfolio/idle"
	"nox/internal/review/sysretro"
	"nox/internal/store/pgstore"
)

// startSleepmon 装配并启动睡钱监控 goroutine（cfg 关则不起——D-145 env 收口）。
func startSleepmon(ctx context.Context, st *pgstore.Store, cfg *config.Config) {
	if !cfg.SleepmonEnabled {
		log.Printf("[sleepmon] disabled by NOX_SLEEPMON_ENABLED")
		return
	}
	safego.GoLoop("sleep_money_mon", func() {
		idle.Run(ctx, idle.Deps{Store: st, InboxPath: sysretro.DefaultInboxPath})
	})
}
