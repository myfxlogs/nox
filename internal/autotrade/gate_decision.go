package autotrade

import (
	"context"
	"fmt"
	"time"
)

// TradeIntent 三 loop 产出的统一开仓意图（D-072 §3.1）。
// Execute 闭包内才调 service.Execute*——三 loop 零直连执行。
type TradeIntent struct {
	Strategy    string // SWAPARB / MEANREVERT / CROSS_VENUE / CARRY / FUNDING_HEDGE / PERP_FUNDING
	Canonical   string
	VenueBuy    string
	VenueSell   string
	Qty         float64
	NotionalUSD float64 // 本笔名义额（swap=余额推导，其余=cfg.NotionalUSD）
	DedupKey    string  // 幂等键（SWAPARB:CANONICAL:vLong:vShort / MEANREVERT:... / detector oppID）
	NetBps      float64 // 开仓时净 bps（日志用）
	Execute     func(ctx context.Context) (status, msg string, posID int64)
}

// GateResult Submit 返回值。
type GateResult struct {
	Status string // EXECUTED / FAILED / REJECTED
	Reason string
	PosID  int64
}

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
	// D-372：同组合 stop/manual 冷却命中（组合级历史抑制，DedupHit 同族；kind/ago 供审计 reason）。
	StopCooldownHit  bool
	StopCooldownKind string
	StopCooldownAgo  time.Duration
	// D-378a S4：组合级止损保险丝命中（C1；count 供审计 reason）。
	ComboLossHit   bool
	ComboLossCount int64
}

// GateVerdict gateDecision 纯函数返回。
type GateVerdict struct {
	Pass             bool
	Reason           string  // 拒绝原因（含关键数值）
	AdjustedNotional float64 // D-168 超限折分：>0 表示需按此名义额折分 qty（Pass=true 时生效）
}

// gateDecision 检查顺序判定（设计 §3.1，纯函数无 IO）。
// 检查顺序即优先级：状态→策略禁用→日亏损→持仓数→名义额度→幂等。
func gateDecision(in GateInput) GateVerdict {
	if in.State != "RUNNING" {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("system state=%s (not RUNNING)", in.State)}
	}
	if in.Disabled {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("strategy %s disabled", in.Strategy)}
	}
	if in.DailyPnl <= -in.MaxDailyLoss {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("daily loss $%.2f ≤ -$%.2f", in.DailyPnl, in.MaxDailyLoss)}
	}
	if in.OpenCount >= in.MaxPositions {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("max positions reached (%d >= %d)", in.OpenCount, in.MaxPositions)}
	}
	// D-108 P3 C1：单笔名义额度判定（持仓数后、幂等前；SkipNotional 跳过 SWAPARB 自管 sizing；
	// MaxNotional≤0=禁用语义保留——未配置或显式关）。
	if !in.SkipNotional && in.MaxNotional > 0 && in.NotionalUSD > in.MaxNotional {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("notional $%.2f > cap $%.2f", in.NotionalUSD, in.MaxNotional)}
	}
	// D-372：止损重进冷却（DedupHit 同组——组合级历史抑制语义族，幂等前）。
	// reason 带 exit_kind 与平仓距今时长便于审计。
	if in.StopCooldownHit {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("combo reentry cooldown (exit_kind=%s, closed %s ago)", in.StopCooldownKind, in.StopCooldownAgo.Round(time.Second))}
	}
	// D-378a S4：组合级止损保险丝（StopCooldownHit 之后、DedupHit 之前——组合级历史抑制族）。
	if in.ComboLossHit {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("combo loss limit (exit_kinds>=%d in window)", in.ComboLossCount)}
	}
	if in.DedupHit {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("duplicate dedup key")}
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
// D-154 S2 加 MedianPnl（小样本判据 median<0 AND avg<0，防 funding 垫正虚高）。
type EliminationInput struct {
	TotalTrades int
	WinRate     float64
	AvgPnl      float64
	MedianPnl   float64
	Disabled    bool
}

// eliminationSampleFloor 小样本噪音保护门槛（D-136）。
// 依据：PERP_FUNDING 日频 ~19 笔，30 笔≈1.5 天即再评估，兼顾灵敏度与噪音保护。
// <此值时单条件（avgPnl<0 或 胜率<30%）易被小样本噪音触发误杀高频微利策略。
const eliminationSampleFloor = 30

// evaluateElimination 策略淘汰判定（从 updateStrategyStat 抽出，设计 §4.3；D-136 判据分级；D-154 S2 中位数判据）。
// 样本量分级：
//
//	<30 笔（小样本噪音保护）：medianPnl<0 且 avgPnl<0 → 禁用（双条件防误杀；winRate 移出——funding/swap 垫正虚高免疫）
//	≥30 笔（大样本，大数定律）：胜率<30% 或 avgPnl<0 任一 → 禁用（单条件可信）
//
// D-154 S2 语义：funding/swap 垫正只能把少数仓推过零线（winRate 虚高），中位数暴露收益结构真相。
// 三锚：D-136 误杀先例（7 笔 winRate=60% avg=-$0.05）多数仓净正→median>0→不杀 ✓；
// PERP_FUNDING 实证（53 笔 33% 胜率 9 负 3 微正）median<0 且 avg<0→杀 ✓；
// SWAPARB 金鹅（swap 日收 net 多数正）median>0→不杀 ✓。
// 返回 disable + reason。
func evaluateElimination(in EliminationInput) (disable bool, reason string) {
	if in.Disabled {
		return false, ""
	}
	if in.TotalTrades < 5 {
		return false, ""
	}
	if in.TotalTrades < eliminationSampleFloor {
		// 小样本：median+avg 双负才杀（D-154 S2，winRate 移出——funding 垫正虚高免疫）
		if in.MedianPnl < 0 && in.AvgPnl < 0 {
			return true, fmt.Sprintf("auto-disabled: %d trades, median=$%.2f, avg=$%.2f", in.TotalTrades, in.MedianPnl, in.AvgPnl)
		}
		return false, ""
	}
	// 大样本：单条件（大数定律，均值/胜率可信）
	if in.WinRate < 0.3 || in.AvgPnl < 0 {
		return true, fmt.Sprintf("auto-disabled: %d trades, winRate=%.0f%%, avg=$%.2f", in.TotalTrades, in.WinRate*100, in.AvgPnl)
	}
	return false, ""
}

// EliminatorAdapter 把 evaluateElimination 包装成 settlement.Eliminator（D-087 §3.2）。
// 供 main 装配时注入 settlement.Settle 的 elim 参数。
// D-154 S2：签名同步扩展 medianPnl 入参（settlement.Eliminator 类型已改）。
func EliminatorAdapter(totalTrades int, winRate, avgPnl, medianPnl float64, disabled bool) (bool, string) {
	return evaluateElimination(EliminationInput{
		TotalTrades: totalTrades,
		WinRate:     winRate,
		AvgPnl:      avgPnl,
		MedianPnl:   medianPnl,
		Disabled:    disabled,
	})
}
