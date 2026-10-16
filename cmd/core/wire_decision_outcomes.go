// wire_decision_outcomes.go —— D-561 裁定归因簿装配：物化（扫描幂等）+ 30d 归因刷新
// + 连败/净亏 warn 边沿推送。30min ticker（裁定低频、归因 30d 滚动——延迟容忍小时级）。
package main

import (
	"context"
	"log"
	"time"

	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

func startDecisionOutcomeRunner(ctx context.Context, st *pgstore.Store) {
	run := func() {
		n, err := st.MaterializeDecisionOutcomes(ctx)
		if err != nil {
			log.Printf("[wiring] decision-outcomes materialize: %v", err)
		} else if n > 0 {
			log.Printf("[wiring] decision-outcomes materialized %d new rows", n)
		}
		alerts, err := st.RefreshDecisionOutcomeAttribution(ctx)
		if err != nil {
			log.Printf("[wiring] decision-outcomes attribution: %v", err)
		} else if alerts > 0 {
			log.Printf("[wiring] decision-outcomes: %d warn alerts pushed", alerts)
		}
	}
	safego.GoLoop("main:decision-outcomes", func() {
		run() // 首拍同步（存量已批裁定回填——首批裁定行落库实拍）
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	})
}
