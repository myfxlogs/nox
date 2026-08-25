package autotrade

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"arb/internal/combo"
	"arb/internal/errclass"
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

// Gate 统一开仓门禁（D-072 §3.1）。三 goroutine 串行化。
// D-080 R1：防重改 DB 实时派生——删内存 openCombos 集，每次 Submit 实时查 ListActivePositions
// 派生组合键匹配。治手动路径旁路（service.go 直接调 pipeline.Execute 不经 gate 的开仓也能被防重）。
type Gate struct {
	mu    sync.Mutex
	store *pgstore.Store
	cfg   Config
}

// NewGate 创建门禁。
func NewGate(store *pgstore.Store, cfg Config) *Gate {
	return &Gate{
		store: store,
		cfg:   cfg,
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

	// 检查 4+6: 活跃持仓数 + 同组合防重（D-080 R1：DB 实时派生，一条 SQL 替代 CountOpenPositions + 内存 openCombos）
	// fail-closed：查询失败 = 拒绝（对齐检查 2-3 风格）
	activePositions, err := g.store.ListActivePositions(ctx)
	if err != nil {
		g.rejectLog(ctx, intent, fmt.Sprintf("list active positions: %v", err))
		return GateResult{Status: "REJECTED", Reason: "active positions check failed"}
	}
	openCount := len(activePositions)
	intentCombo := combo.FromOppID(intent.DedupKey)
	comboAlreadyOpen := false
	for _, p := range activePositions {
		if p.OpportunityID == "" {
			continue
		}
		if combo.MatchOppID(p.OpportunityID, intentCombo) {
			comboAlreadyOpen = true
			break
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
		NotionalUSD:  intent.NotionalUSD,
		MaxNotional:  g.cfg.MaxNotional,
		// SWAPARB 豁免 notional cap：D-096 判定书（净算月~$110=年化1.8%无杠杆，不注资）+
		// D-097 自管 sizing（SwapArbBareCap 1 手 + minNotional skip；swap 收租需整手，
		// MaxNotional 单笔风险 cap 语义不适用——整手收租非单笔风险敞口）。
		SkipNotional: intent.Strategy == "SWAPARB",
		DedupHit:     comboAlreadyOpen,
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
		// D-080 S1：同组合防重拒绝原因覆盖（gateDecision 返回通用 "duplicate dedup key"）
		if comboAlreadyOpen {
			verdict.Reason = "combo already open"
		}
		g.rejectLog(ctx, intent, verdict.Reason)
		return GateResult{Status: "REJECTED", Reason: verdict.Reason}
	}

	// 执行
	status, msg, posID := intent.Execute(ctx)

	logEntry.PositionID = nil
	if posID > 0 {
		logEntry.PositionID = &posID
	}
	logEntry.Status = status
	logEntry.Reason = msg
	// D-077 修复⑩ G3：执行失败按 errclass 分类落库（reason 前缀 Retry:/RetryFresh:/Abort:/Halt:）。
	// Halt 类（MARKET_CLOSED/TRADE_DISABLED）→ DisableStrategy + critical 告警。
	// Abort 类计数日志。Retry/RetryFresh v1 不做自动重试（留触发条件）。
	if status == "FAILED" {
		action := g.classifyFailure(intent, msg)
		logEntry.Reason = action.String() + ": " + errclass.TruncateNote(msg, 120)
		if action == errclass.Halt {
			g.haltStrategy(ctx, intent, logEntry.Reason)
		} else if action == errclass.Abort {
			log.Printf("[autotrade] gate Abort %s: %s", intent.DedupKey, msg)
		}
	}
	_ = g.store.InsertAutotradeLog(ctx, logEntry)

	if status == "FAILED" {
		log.Printf("[autotrade] gate FAILED %s: %s", intent.DedupKey, logEntry.Reason)
	} else {
		log.Printf("[autotrade] gate EXECUTED %s posID=%d", intent.DedupKey, posID)
	}
	return GateResult{Status: status, Reason: logEntry.Reason, PosID: posID}
}

// classifyFailure 按双腿 venue 分类执行失败（D-077 修复⑩ G3）。
// 取两腿中更严重的 Action（Halt > RetryFresh > Retry > Abort）。
func (g *Gate) classifyFailure(intent TradeIntent, msg string) errclass.Action {
	a1 := errclass.ClassifyErr(intent.VenueBuy, errors.New(msg))
	a2 := errclass.ClassifyErr(intent.VenueSell, errors.New(msg))
	return worseAction(a1, a2)
}

// worseAction 返回更严重的 Action（Halt > RetryFresh > Retry > Abort）。
func worseAction(a, b errclass.Action) errclass.Action {
	severity := map[errclass.Action]int{
		errclass.Abort:      0,
		errclass.Retry:      1,
		errclass.RetryFresh: 2,
		errclass.Halt:       3,
	}
	if severity[a] >= severity[b] {
		return a
	}
	return b
}

// haltStrategy Halt 类失败 → DisableStrategy + critical 告警（D-077 修复⑩ G3）。
func (g *Gate) haltStrategy(ctx context.Context, intent TradeIntent, reason string) {
	if err := g.store.DisableStrategy(ctx, intent.Strategy, reason); err != nil {
		log.Printf("[autotrade] halt DisableStrategy %s: %v", intent.Strategy, err)
	}
	alertMsg := fmt.Sprintf("strategy %s HALTED: %s", intent.Strategy, reason)
	_ = g.store.InsertAlert(ctx, nil, "critical", alertMsg)
	log.Printf("[autotrade] HALT %s: %s", intent.Strategy, reason)
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
