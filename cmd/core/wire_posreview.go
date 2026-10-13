// wire_posreview.go —— D-266 M4.5：复盘员装配（posChange 订阅+ResolveClient 复用）。
package main

import (
	"context"

	"nox/internal/platform/analyst"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/review/kbreport"
	"nox/internal/review/posreview"
	"nox/internal/store/pgstore"
)

// wirePosreview 复盘员事件循环：订 posChange（positions 写提交即发=全平仓径覆盖），
// boot 首扫补存量 backlog。ResolveClient=kbreport 口径 DB 优先（零新 env）；
// CallLog 落 fin_llm_call role=reviewer。
func wirePosreview(ctx context.Context, st *pgstore.Store, cfg *config.Config, a monitorsSetupArgs) {
	safego.GoLoop("main:posreview", func() {
		ch, cancel := a.posChange.Subscribe()
		defer cancel()
		posreview.Run(ctx, posreview.Deps{
			Store: st,
			Beat:  func() { safego.Beat("main:posreview") },
			Resolve: func(ctx context.Context) (*analyst.Client, error) {
				c, err := kbreport.ResolveClient(ctx, st, cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
				if c != nil {
					c.CallLog = analyst.LLMLogger(st, "reviewer") // D-246/D-266：fin_llm_call 落行
				}
				return c, err
			},
		}, ch)
	})
}
