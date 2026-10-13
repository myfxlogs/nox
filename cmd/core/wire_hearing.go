package main

import (
	"context"

	"nox/internal/platform/analyst"
	"nox/internal/platform/config"
	"nox/internal/review/hearing"
	"nox/internal/review/kbreport"
	"nox/internal/store/pgstore"
)

// newElimHearing D-249①：淘汰多空听证钩——结算径+startup 淘汰扫两装配点共用。
// ResolveClient 口径同 deptreport（DB 优先 env 回落）；role=elim_hearing 入
// fin_llm_call 账本（§4.3 听证角色）。AI 未配置=听证缺如正常淘汰（fail-open）。
func newElimHearing(st *pgstore.Store, cfg *config.Config) func(
	ctx context.Context, trigger string, fam []string,
	total int, winRate, avgPnl, medianPnl float64, reason string) {
	d := &hearing.Deps{
		Store: st,
		ResolveClient: func(ctx context.Context) (*analyst.Client, error) {
			c, err := kbreport.ResolveClient(ctx, st, cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
			if c != nil {
				c.CallLog = analyst.LLMLogger(st, "elim_hearing")
			}
			return c, err
		},
	}
	return func(ctx context.Context, trigger string, fam []string,
		total int, winRate, avgPnl, medianPnl float64, reason string) {
		d.Debate(ctx, trigger, fam, hearing.Stats{
			TotalTrades: total, WinRate: winRate, AvgPnl: avgPnl, MedianPnl: medianPnl,
		}, reason)
	}
}
