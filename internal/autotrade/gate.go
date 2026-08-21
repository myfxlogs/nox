package autotrade

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"arb/internal/store/pgstore"
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
	DedupHit     bool    // 幂等命中
}

// GateVerdict gateDecision 纯函数返回。
type GateVerdict struct {
	Pass   bool
	Reason string // 拒绝原因（含关键数值）
}

// gateDecision 检查顺序判定（设计 §3.1，纯函数无 IO）。
// 检查顺序即优先级：状态→策略禁用→日亏损→持仓数→幂等。
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

// evaluateElimination 策略淘汰判定（从 updateStrategyStat 抽出，设计 §4.3）。
// ≥5 笔且 (胜率<30% 或 avg_pnl<0) → 禁用。返回 disable + reason。
func evaluateElimination(in EliminationInput) (disable bool, reason string) {
	if in.Disabled {
		return false, ""
	}
	if in.TotalTrades < 5 {
		return false, ""
	}
	if in.WinRate < 0.3 {
		return true, fmt.Sprintf("auto-disabled: %d trades, winRate=%.0f%%, avgPnl=$%.2f", in.TotalTrades, in.WinRate*100, in.AvgPnl)
	}
	if in.AvgPnl < 0 {
		return true, fmt.Sprintf("auto-disabled: %d trades, winRate=%.0f%%, avgPnl=$%.2f", in.TotalTrades, in.WinRate*100, in.AvgPnl)
	}
	return false, ""
}

// Gate 统一开仓门禁（D-072 §3.1）。三 goroutine 串行化。
type Gate struct {
	mu       sync.Mutex
	store    *pgstore.Store
	cfg      Config
	executed map[string]bool // 会话内幂等
}

// NewGate 创建门禁。
func NewGate(store *pgstore.Store, cfg Config) *Gate {
	return &Gate{
		store:    store,
		cfg:      cfg,
		executed: make(map[string]bool),
	}
}

// Submit 统一开仓入口。全程持锁串行化（设计 §3.1）。
// 拒绝路径也落 autotrade_logs（三路径统一审计）。
func (g *Gate) Submit(ctx context.Context, intent TradeIntent) GateResult {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 读取当前状态
	stateRow, err := g.store.GetAutotradeState(ctx)
	if err != nil {
		// fail-closed（D-072 复审修正）：状态不可知 = 拒绝开仓，禁止默认 RUNNING 放行
		g.rejectLog(ctx, intent, fmt.Sprintf("get state: %v", err))
		return GateResult{Status: "REJECTED", Reason: "get autotrade state failed"}
	}
	state := stateRow.State

	// 检查 2: 策略禁用（fail-closed：查询失败 = 拒绝）
	disabled, err := g.store.IsStrategyDisabled(ctx, intent.Strategy)
	if err != nil {
		g.rejectLog(ctx, intent, fmt.Sprintf("check strategy disabled: %v", err))
		return GateResult{Status: "REJECTED", Reason: "strategy disabled check failed"}
	}

	// 检查 3: 日亏损（fail-closed：查询失败 = 拒绝）
	dailyPnl, err := g.store.DailyRealizedPnl(ctx)
	if err != nil {
		g.rejectLog(ctx, intent, fmt.Sprintf("daily pnl: %v", err))
		return GateResult{Status: "REJECTED", Reason: "daily pnl check failed"}
	}

	// 检查 4: 持仓数（fail-closed：查询失败 = 拒绝）
	openCount, err := g.store.CountOpenPositions(ctx)
	if err != nil {
		g.rejectLog(ctx, intent, fmt.Sprintf("count open positions: %v", err))
		return GateResult{Status: "REJECTED", Reason: "open positions check failed"}
	}

	// 检查 6: 幂等（会话内 + DB 级）
	dedupHit := g.executed[intent.DedupKey]
	if !dedupHit {
		// DB 级幂等：detector 用 HasOpenPositionByOpportunity，MR/SWAPARB 用 LIKE 前缀
		if intent.Strategy == "SWAPARB" || intent.Strategy == "MEANREVERT" {
			positions, _ := g.store.ListPositions(ctx, "OPEN", 200, 0)
			prefix := intent.DedupKey
			for _, p := range positions {
				if len(p.OpportunityID) >= len(prefix) && p.OpportunityID[:len(prefix)] == prefix {
					dedupHit = true
					break
				}
			}
		} else {
			// detector 机会用 oppID 作为 DedupKey
			has, _ := g.store.HasOpenPositionByOpportunity(ctx, intent.DedupKey)
			dedupHit = has
		}
	}

	verdict := gateDecision(GateInput{
		State:        state,
		Strategy:     intent.Strategy,
		Disabled:     disabled,
		DailyPnl:     dailyPnl,
		MaxDailyLoss: g.cfg.MaxDailyLoss,
		OpenCount:    openCount,
		MaxPositions: g.cfg.MaxPositions,
		DedupHit:     dedupHit,
	})

	// 构造日志条目（三路径统一审计）
	logEntry := &pgstore.AutotradeLog{
		OpportunityID: intent.DedupKey,
		Strategy:      intent.Strategy,
		Canonical:     intent.Canonical,
		VenueBuy:      intent.VenueBuy,
		VenueSell:     intent.VenueSell,
		Qty:           intent.Qty,
		NetBps:        intent.NetBps,
	}

	if !verdict.Pass {
		// 检查 3 触发 DAILY_LOSS_HALT
		if verdict.Reason != "" && state == StateRunning && dailyPnl <= -g.cfg.MaxDailyLoss {
			haltedOn := time.Now().UTC().Truncate(24 * time.Hour)
			_ = g.store.SetAutotradeState(ctx, StateDailyLossHalt, verdict.Reason, &haltedOn)
		}
		g.rejectLog(ctx, intent, verdict.Reason)
		return GateResult{Status: "REJECTED", Reason: verdict.Reason}
	}

	// 执行
	status, msg, posID := intent.Execute(ctx)
	g.executed[intent.DedupKey] = true

	logEntry.PositionID = nil
	if posID > 0 {
		logEntry.PositionID = &posID
	}
	logEntry.Status = status
	logEntry.Reason = msg
	_ = g.store.InsertAutotradeLog(ctx, logEntry)

	if status == "FAILED" {
		log.Printf("[autotrade] gate FAILED %s: %s", intent.DedupKey, msg)
	} else {
		log.Printf("[autotrade] gate EXECUTED %s posID=%d", intent.DedupKey, posID)
	}
	return GateResult{Status: status, Reason: msg, PosID: posID}
}

// rejectLog 拒绝路径统一落库 + 日志（fail-closed 检查失败与规则拒绝共用）。
func (g *Gate) rejectLog(ctx context.Context, intent TradeIntent, reason string) {
	_ = g.store.InsertAutotradeLog(ctx, &pgstore.AutotradeLog{
		OpportunityID: intent.DedupKey,
		Strategy:      intent.Strategy,
		Canonical:     intent.Canonical,
		VenueBuy:      intent.VenueBuy,
		VenueSell:     intent.VenueSell,
		Qty:           intent.Qty,
		Status:        "REJECTED",
		Reason:        reason,
		NetBps:        intent.NetBps,
	})
	log.Printf("[autotrade] gate REJECT %s: %s", intent.DedupKey, reason)
}
