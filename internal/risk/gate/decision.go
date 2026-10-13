package gate

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

// Submitter 开仓闸消费面（D-262 M4.3①）——dashboard 手动执行面注入签名同源，
// autotrade.New 内经接口断言把 Gate 同实例挂进 service 侧，跨包零循环依赖。
type Submitter interface {
	Submit(ctx context.Context, intent *TradeIntent) GateResult
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
	// 同组合 stop/manual 冷却命中（组合级历史抑制，DedupHit 同族；kind/ago 供审计 reason）。
	StopCooldownHit  bool
	StopCooldownKind string
	StopCooldownAgo  time.Duration
	// 组合级止损保险丝命中（C1；count 供审计 reason）。
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
	// 单笔名义额度判定（持仓数后、幂等前；SkipNotional 跳过 SWAPARB 自管 sizing；
	// MaxNotional≤0=禁用语义保留——未配置或显式关）。
	if !in.SkipNotional && in.MaxNotional > 0 && in.NotionalUSD > in.MaxNotional {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("notional $%.2f > cap $%.2f", in.NotionalUSD, in.MaxNotional)}
	}
	// 止损重进冷却（DedupHit 同组——组合级历史抑制语义族，幂等前）。
	// reason 带 exit_kind 与平仓距今时长便于审计。
	if in.StopCooldownHit {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("combo reentry cooldown (exit_kind=%s, closed %s ago)", in.StopCooldownKind, in.StopCooldownAgo.Round(time.Second))}
	}
	// 组合级止损保险丝（StopCooldownHit 之后、DedupHit 之前——组合级历史抑制族）。
	if in.ComboLossHit {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("combo loss limit (exit_kinds>=%d in window)", in.ComboLossCount)}
	}
	if in.DedupHit {
		return GateVerdict{Pass: false, Reason: "duplicate dedup key"}
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
func NextState(cur string, dailyPnl, maxDailyLoss float64, allDisabled bool, utcDate time.Time, haltedOn *time.Time) (next, reason string) {
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
