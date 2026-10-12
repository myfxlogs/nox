// wire_kbreport.go —— D-442 S3：KB 元分析周报器装配。
// 周定时器（对齐 UTC 周一 00:00 后首 tick）+ kbreport.Generate + InsertAlert 完成通知。
// 独立限流：自有 NewClient 实例，不占 AnalystRatePerHour 前端限流器（周一次量级）。
package main

import (
	"context"
	"log"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/review/kbreport"
	"nox/internal/store/pgstore"
)

// kbReportInterval 周报周期（包级 var，测试可缩短）。
var kbReportInterval = 7 * 24 * time.Hour

// startKbReporter 装配并启动周报 goroutine。
func startKbReporter(ctx context.Context, st *pgstore.Store, cfg *config.Config) {
	tools := &analyst.Tools{ReadOnlyDSN: cfg.AIPGDSN, Store: st, KnRoot: analyst.KnRoot}
	safego.GoLoop("main:kbReport", func() { kbReportLoop(ctx, st, cfg, tools) })
}

// kbReportLoop 周一 00:00 UTC 后首 tick → 每周 Generate；err→log warn 不炸（下周再试）。
func kbReportLoop(ctx context.Context, st *pgstore.Store, cfg *config.Config, tools analyst.ToolExecutor) {
	timer := time.NewTimer(delayUntilNextMondayUTC(time.Now().UTC()))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		client, err := kbreport.ResolveClient(ctx, st, cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
		if err != nil {
			log.Printf("[kbreport] resolve client: %v", err)
		} else if path, err := kbreport.Generate(ctx, kbreport.Deps{Client: client, Tools: tools,
			Alert: func(level, msg string) { _ = st.InsertAlert(context.Background(), nil, level, msg) }}); err != nil {
			log.Printf("[kbreport] generate: %v", err)
		} else {
			// 完成 → Bell 告警通道闭环通知（报告文件落工作树，由下个触碰席位 commit，同待归纳先例）。
			if err := st.InsertAlert(context.Background(), nil, "info", "[KB] 元报告已生成："+path); err != nil {
				log.Printf("[kbreport] insert alert: %v", err)
			}
		}
		timer.Reset(kbReportInterval)
	}
}

// delayUntilNextMondayUTC 距下一个周一 00:00 UTC 的时长（now 恰为周一 00:00 则立即触发）。
func delayUntilNextMondayUTC(now time.Time) time.Duration {
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for next.Weekday() != time.Monday || next.Before(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(now)
}
