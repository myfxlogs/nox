// setup_swap.go —— D-110 P2：swap 对账告警装配（自 setup.go 拆分，纯移动零语义改）。
package main

import (
	"context"

	"nox/internal/api/dashboard"
	"nox/internal/ledger/swapledger"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// wireSwapReconciler 日频 swap 对账 loop（公式值 vs MT5 实际增量偏差>阈值告警）。
func wireSwapReconciler(ctx context.Context, st *pgstore.Store, cfg *config.Config, dashSvc *dashboard.Service) {
	// 8d. swap 对账告警（D-110 P2）：日频 ticker，公式值 vs MT5 实际增量偏差>阈值告警。
	// initialDelay=2h（晚于 swapledger 90s，确保当日已记账）。
	swapReconciler := swapledger.NewSwapReconciler(st, cfg.SwapReconcileDeviationPct)
	swapReconciler.SetNotional(dashSvc) // D-161：窗径名义源（USDPerLotForLeg；缺=组回退日频旧径）
	safego.Go("setup:swapReconciler", func() { swapReconciler.StartLoop(ctx) })

}
