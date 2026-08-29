package autotrade

import (
	"fmt"
	"time"
)

// GateInput gateDecision 纯函数入参（可测性，无 IO）。
type GateInput struct {
	State        string  // 当前系统状态
	Strategy     string  // 策略类型
	Disabled     bool    // 策略是否禁用
	DailyPnl     float64 // 当日已实现 PnL
	MaxDailyLoss float64 // 日亏上限（正值，如 100）
	OpenCount    int     // 当前开仓数
	MaxPositions int     // 最大持仓数
	NotionalUSD  float64 // 本笔名义价值（USD，D-108 P3 C1）
	MaxNotional  float64 // 单笔名义上限（USD，>0 启用，≤0 禁用）
	SkipNotional bool    // 跳过 notional 判定（SWAPARB 自管 sizing 豁免，D-096/D-097）
	DedupHit     bool    // 幂等命中
}

// GateVerdict gateDecision 纯函数返回。
type GateVerdict struct {
	Pass   bool
	Reason string // 拒绝原因（含关键数值）
}

// gateDecision 检查顺序判定（设计 §3.1，纯函数无 IO）。
// 检查顺序即优先级：状态→策略禁用→日亏损→持仓数→名义额度→幂等。
func gateDecision(in GateInput) GateVerdict {
	if in.State != "RUNNING" {
		return GateVerdict{false, fmt.Sprintf("system state=%s (not RUNNING)", in.State)}
	}
	if in.Disabled {
		return GateVerdict{false, fmt.Sprintf("strategy %s disabled", in.Strategy)}
	}
	if in.DailyPnl <= -in.MaxDailyLoss {
		return GateVerdict{false, fmt.Sprintf("daily loss $%.2f ≤ -$%.2f", in.DailyPnl, in.MaxDailyLoss)}
	}
	if in.OpenCount >= in.MaxPositions {
		return GateVerdict{false, fmt.Sprintf("max positions reached (%d >= %d)", in.OpenCount, in.MaxPositions)}
	}
	// D-108 P3 C1：单笔名义额度判定（持仓数后、幂等前；SkipNotional 跳过 SWAPARB 自管 sizing；
	// MaxNotional≤0=禁用语义保留——未配置或显式关）。
	if !in.SkipNotional && in.MaxNotional > 0 && in.NotionalUSD > in.MaxNotional {
		return GateVerdict{false, fmt.Sprintf("notional $%.2f > cap $%.2f", in.NotionalUSD, in.MaxNotional)}
	}
	if in.DedupHit {
		return GateVerdict{false, fmt.Sprintf("duplicate dedup key")}
	}
	return GateVerdict{Pass: true}
}

// AutotradeState 状态机常量（D-072 §3.2 四态）。
const (
	StateRunning           = "RUNNING"
	StateDailyLossHalt     = "DAILY_LOSS_HALT"
	StateStrategyExhausted = "STRATEGY_EXHAUSTED"
	StateCloseOnly         = "CLOSE_ONLY"
)

// nextState 状态迁移纯函数（设计 §3.2 四态转换表）。
// cur=当前状态, dailyPnl=当日盈亏, allDisabled=全部策略禁用, utcDate=当前UTC日期, haltedOn=停机日期。
// 返回 next 状态 + reason。仅 RUNNING↔DAILY_LOSS_HALT 自动；STRATEGY_EXHAUSTED/CLOSE_ONLY 仅人工。
func nextState(cur string, dailyPnl, maxDailyLoss float64, allDisabled bool, utcDate time.Time, haltedOn *time.Time) (next, reason string) {
	switch cur {
	case StateRunning:
		if dailyPnl <= -maxDailyLoss {
			return StateDailyLossHalt, fmt.Sprintf("daily loss $%.2f ≤ -$%.2f", dailyPnl, maxDailyLoss)
		}
		if allDisabled {
			return StateStrategyExhausted, "all strategies disabled"
		}
		return StateRunning, ""
	case StateDailyLossHalt:
		// 跨 UTC 日自动恢复
		if haltedOn != nil && utcDate.Sub(*haltedOn) >= 24*time.Hour {
			return StateRunning, "new UTC day, auto-recover"
		}
		return StateDailyLossHalt, ""
	case StateStrategyExhausted:
		// 仅人工恢复
		return StateStrategyExhausted, ""
	case StateCloseOnly:
		// 仅人工恢复
		return StateCloseOnly, ""
	}
	return cur, ""
}

// EliminationInput evaluateElimination 纯函数入参。
type EliminationInput struct {
	TotalTrades int
	WinRate     float64
	AvgPnl      float64
	Disabled    bool
}

// eliminationSampleFloor 小样本噪音保护门槛（D-136）。
// 依据：PERP_FUNDING 日频 ~19 笔，30 笔≈1.5 天即再评估，兼顾灵敏度与噪音保护。
// <此值时单条件（avgPnl<0 或 胜率<30%）易被小样本噪音触发误杀高频微利策略。
const eliminationSampleFloor = 30

// evaluateElimination 策略淘汰判定（从 updateStrategyStat 抽出，设计 §4.3；D-136 判据分级）。
// 样本量分级：
//
//	<30 笔（小样本噪音保护）：胜率<30% 且 avgPnl<0 → 禁用（双条件防误杀）
//	≥30 笔（大样本，大数定律）：胜率<30% 或 avgPnl<0 任一 → 禁用（单条件可信）
//
// 返回 disable + reason。
func evaluateElimination(in EliminationInput) (disable bool, reason string) {
	if in.Disabled {
		return false, ""
	}
	if in.TotalTrades < 5 {
		return false, ""
	}
	reasonFmt := "auto-disabled: %d trades, winRate=%.0f%%, avgPnl=$%.2f"
	if in.TotalTrades < eliminationSampleFloor {
		// 小样本：双条件（噪音保护，D-136 修复①——PERP_FUNDING 7 笔 winRate=60% avg=-$0.05 误杀先例）
		if in.WinRate < 0.3 && in.AvgPnl < 0 {
			return true, fmt.Sprintf(reasonFmt, in.TotalTrades, in.WinRate*100, in.AvgPnl)
		}
		return false, ""
	}
	// 大样本：单条件（大数定律，均值/胜率可信）
	if in.WinRate < 0.3 || in.AvgPnl < 0 {
		return true, fmt.Sprintf(reasonFmt, in.TotalTrades, in.WinRate*100, in.AvgPnl)
	}
	return false, ""
}

// EliminatorAdapter 把 evaluateElimination 包装成 settlement.Eliminator（D-087 §3.2）。
// 供 main 装配时注入 settlement.Settle 的 elim 参数。
func EliminatorAdapter(totalTrades int, winRate, avgPnl float64, disabled bool) (bool, string) {
	return evaluateElimination(EliminationInput{
		TotalTrades: totalTrades,
		WinRate:     winRate,
		AvgPnl:      avgPnl,
		Disabled:    disabled,
	})
}
