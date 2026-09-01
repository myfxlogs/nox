package autotrade

import (
	"context"
	"fmt"
	"log"
	"time"
)

// stateMachineTick 状态机每 tick 检查（D-072 §3.2 + §4.3 首跑淘汰）。
func (e *Engine) stateMachineTick(ctx context.Context) {
	// 首跑淘汰：回填后立即对所有策略跑一次淘汰判定
	if !e.firstTickDone {
		e.firstTickDone = true
		e.runStartupElimination(ctx)
	}

	// 读取当前状态
	stateRow, err := e.store.GetAutotradeState(ctx)
	if err != nil {
		return
	}

	// 跨日恢复 + 全策略禁用检查
	dailyPnl, _ := e.store.DailyRealizedPnl(ctx)
	allDisabled := e.checkAllStrategiesDisabled(ctx)
	utcNow := time.Now().UTC().Truncate(24 * time.Hour)

	next, reason := nextState(stateRow.State, dailyPnl, e.cfg.MaxDailyLoss, allDisabled, utcNow, stateRow.HaltedOn)
	if next != stateRow.State {
		var haltedOn *time.Time
		if next == StateDailyLossHalt {
			t := utcNow
			haltedOn = &t
		}
		// D-189 P0-3：状态写入失败显式处理——日志 + critical alert（alert 失败也 log）。
		// 不假设迁移成功；下一 tick 重新读取并重试。读取状态/日亏/策略禁用任一失败仍按现有 fail-closed 跳过。
		if err := e.store.SetAutotradeState(ctx, next, reason, haltedOn); err != nil {
			msg := fmt.Sprintf("SetAutotradeState %s→%s failed (will re-read next tick): %v", stateRow.State, next, err)
			log.Printf("[autotrade] %s", msg)
			if alertErr := e.store.InsertAlert(ctx, nil, "critical", msg); alertErr != nil {
				log.Printf("[autotrade] state tick alert insert also failed: %v", alertErr)
			}
		} else {
			log.Printf("[autotrade] state %s → %s: %s", stateRow.State, next, reason)
		}
	}
}

// checkAllStrategiesDisabled 检查全部策略是否被禁用。
func (e *Engine) checkAllStrategiesDisabled(ctx context.Context) bool {
	strategies := []string{"CROSS_VENUE", "MEANREVERT", "SWAPARB", "CARRY", "FUNDING_HEDGE", "PERP_FUNDING"}
	for _, s := range strategies {
		disabled, err := e.store.IsStrategyDisabled(ctx, s)
		if err == nil && !disabled {
			return false
		}
	}
	return true
}

// runStartupElimination 启动首跑淘汰判定（D-072 §4.3，回填后立即生效）。
// D-154 S1 裁决①：输入源改 AggregateStrategyStats 实时聚合（与 settlement.go 同源 P3）；disabled 仍查 strategy_stats。
func (e *Engine) runStartupElimination(ctx context.Context) {
	strategies := []string{"CROSS_VENUE", "MEANREVERT", "SWAPARB", "CARRY", "FUNDING_HEDGE", "PERP_FUNDING"}
	for _, s := range strategies {
		stat, err := e.store.GetStrategyStat(ctx, s)
		if err != nil {
			continue
		}
		agg, err := e.store.AggregateStrategyStats(ctx, s)
		if err != nil {
			log.Printf("[autotrade] startup elimination aggregate %s: %v", s, err)
			continue
		}
		winRate := 0.0
		if agg.Total > 0 {
			winRate = float64(agg.Wins) / float64(agg.Total)
		}
		disable, reason := evaluateElimination(EliminationInput{
			TotalTrades: agg.Total,
			WinRate:     winRate,
			AvgPnl:      agg.AvgPnl,
			MedianPnl:   agg.MedianPnl,
			Disabled:    stat.Disabled,
		})
		if disable {
			if err := e.store.DisableStrategy(ctx, s, reason); err != nil {
				log.Printf("[autotrade] startup elimination disable %s: %v", s, err)
			} else {
				log.Printf("[autotrade] STARTUP ELIMINATION: %s disabled: %s", s, reason)
			}
		}
	}
}

// updateStrategyStat 已被 settlement.Settle 取代（D-087 §3.2，旧对象 no-op 缺陷根除）。
// 删除——不留死代码。淘汰判定经 settlement.Eliminator 注入。
