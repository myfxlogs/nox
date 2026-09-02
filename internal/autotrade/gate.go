package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"
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
	// D-195-A：执行控制面（execution_controls 是执行模式唯一真相源）。
	controlPlane *ControlPlane
	// D-154 S3：disabled 拒绝 stdout 1h 去重（复用 D-144 ② per-key 1h 模式）。
	// DB 落库保留全量（审计完整性不动），只压 stdout。内存，重启重置。
	lastDisabledLog   map[string]time.Time
	lastDisabledLogMu sync.Mutex
}

// NewGate 创建门禁。
// D-195-A：新增 controlPlane 参数（autotrade.New 内部自建，签名扩展第 3 参数）。
func NewGate(store *pgstore.Store, cfg Config, controlPlane *ControlPlane) *Gate {
	return &Gate{
		store:           store,
		cfg:             cfg,
		controlPlane:    controlPlane,
		lastDisabledLog: make(map[string]time.Time),
	}
}

// Submit 统一开仓入口。全程持锁串行化（设计 §3.1）。
// 拒绝路径也落 autotrade_logs（三路径统一审计）。
func (g *Gate) Submit(ctx context.Context, intent *TradeIntent) GateResult {
	g.mu.Lock()
	defer g.mu.Unlock()

	// D-195-A：统一执行模式检查（execution_controls 是唯一真相源）
	// 首版只用 global scope；account 级覆盖留后续工作包。
	if g.controlPlane != nil {
		mode, allowed, cpReason, cpErr := g.controlPlane.CheckGate(ctx)
		if cpErr != nil || !allowed {
			g.rejectLog(ctx, *intent, fmt.Sprintf("control plane: %v (mode=%s)", cpErr, mode), false)
			return GateResult{Status: "REJECTED", Reason: "execution mode blocked: " + cpReason}
		}
	}

	// 读取当前状态
	stateRow, err := g.store.GetAutotradeState(ctx)
	if err != nil {
		// fail-closed（D-072 复审修正）：状态不可知 = 拒绝开仓，禁止默认 RUNNING 放行
		g.rejectLog(ctx, *intent, fmt.Sprintf("get state: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "get autotrade state failed"}
	}
	state := stateRow.State

	// 检查 2: 策略禁用（fail-closed：查询失败 = 拒绝）
	disabled, err := g.store.IsStrategyDisabled(ctx, intent.Strategy)
	if err != nil {
		g.rejectLog(ctx, *intent, fmt.Sprintf("check strategy disabled: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "strategy disabled check failed"}
	}

	// 检查 3: 日亏损（fail-closed：查询失败 = 拒绝）
	dailyPnl, err := g.store.DailyRealizedPnl(ctx)
	if err != nil {
		g.rejectLog(ctx, *intent, fmt.Sprintf("daily pnl: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "daily pnl check failed"}
	}

	// 检查 4+6: 活跃持仓数 + 同组合防重（D-080 R1：DB 实时派生，一条 SQL 替代 CountOpenPositions + 内存 openCombos）
	// fail-closed：查询失败 = 拒绝（对齐检查 2-3 风格）
	activePositions, err := g.store.ListActivePositions(ctx)
	if err != nil {
		g.rejectLog(ctx, *intent, fmt.Sprintf("list active positions: %v", err), false)
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
		SkipNotional: intent.Strategy == "SWAPARB" || intent.Strategy == "CARRY",
		DedupHit:     comboAlreadyOpen,
	})

	// D-159 W1a R1：精确组合门禁（SWAPARB/CARRY 共用，gateDecision 后、Execute 前拦截）。
	// D-187：CARRY 合并 SWAPARB 统一门禁栈——comboAllowVerdict/gross split 对 CARRY 同样生效。
	// gateDecision 已过（state=RUNNING、未禁用、未超仓、未超 notional、未 dedup）才进入此检查。
	// 非 RUNNING 状态（CLOSE_ONLY 等）已在 gateDecision 拒绝，不会到此。
	if intent.Strategy == "SWAPARB" || intent.Strategy == "CARRY" {
		// D-179 S3：预扫活跃 SWAPARB/CARRY 仓 notional_usd NULL（combo_allow.go 纯函数不打日志，warn 在此调用点）。
		for _, p := range activePositions {
			if p.OpportunityID != "" && (strings.HasPrefix(p.OpportunityID, "SWAPARB:") || strings.HasPrefix(p.OpportunityID, "CARRY:")) && p.NotionalUSD == nil {
				log.Printf("[autotrade] WARN position %d notional_usd NULL (backfill missing?) counted as 0", p.ID)
			}
		}
		cv := comboAllowVerdict(g.cfg.SwapArbAllowCombos, g.cfg.SwapArbDenyCombos, g.cfg.SwapArbAutoAllow, g.cfg.MaxActiveCombos, g.cfg.SwapArbMaxGrossExposure, *intent, activePositions)
		if !cv.Pass {
			g.rejectLog(ctx, *intent, cv.Reason, false)
			return GateResult{Status: "REJECTED", Reason: cv.Reason}
		}
		// D-168 超限折分：AdjustedNotional>0 表示需按剩余额度折分 qty
		// D-179 S4：折分 qty 向下规整到 0.01 步长（FX min lot），<0.01 拒开（宁可不开不可超限）。
		if cv.AdjustedNotional > 0 && intent.NotionalUSD > 0 {
			ratio := cv.AdjustedNotional / intent.NotionalUSD
			originalQty := intent.Qty
			originalNotional := intent.NotionalUSD
			splitQty := intent.Qty * ratio
			flooredQty, reject := grossSplitFloor(splitQty)
			if reject {
				reason := fmt.Sprintf("gross adjusted qty %.4f below min lot floor", splitQty)
				g.rejectLog(ctx, *intent, reason, false)
				return GateResult{Status: "REJECTED", Reason: reason}
			}
			intent.Qty = flooredQty
			// 重算 NotionalUSD 保持量价一致（规整后 qty 对应名义）
			intent.NotionalUSD = cv.AdjustedNotional * flooredQty / splitQty
			log.Printf("[autotrade] gate gross split: %s qty %.4f → %.4f, notional $%.2f → $%.2f (%s)",
				intent.DedupKey, originalQty, intent.Qty, originalNotional, intent.NotionalUSD, cv.Reason)
		}
	}

	// D-159 W1a I6：environment/is_real 一致性检查（所有策略，gateDecision 后）。
	// 校验 venue 的 broker_accounts.is_real 与 g.cfg.Environment 一致：
	//   is_real=true ↔ environment='live' / is_real=false ↔ environment='demo'
	//   is_real=nil（未知）→ fail-closed 拒绝（不允许未知环境开仓）。
	// Environment 空 = 未配置（不检查，回退旧行为）。
	if g.cfg.Environment != "" {
		if rv := g.environmentIsRealCheck(ctx, *intent); !rv.Pass {
			g.rejectLog(ctx, *intent, rv.Reason, false)
			return GateResult{Status: "REJECTED", Reason: rv.Reason}
		}
	}

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
			// D-189 P0-3：状态写入失败显式处理——日志 + critical alert（alert 失败也 log）。
			// 不增加 inMemoryHalt（Gate 每次 Submit 重新读状态/日亏/策略禁用，内存 latch 无恢复协议+重复防线）。
			if err := g.store.SetAutotradeState(ctx, StateDailyLossHalt, verdict.Reason, &haltedOn); err != nil {
				msg := fmt.Sprintf("SetAutotradeState DAILY_LOSS_HALT failed (fail-closed, will re-read next tick): %v", err)
				log.Printf("[autotrade] %s", msg)
				if alertErr := g.store.InsertAlert(ctx, nil, "critical", msg); alertErr != nil {
					log.Printf("[autotrade] DAILY_LOSS_HALT alert insert also failed: %v", alertErr)
				}
			}
		}
		// D-080 S1：同组合防重拒绝原因覆盖（gateDecision 返回通用 "duplicate dedup key"）
		if comboAlreadyOpen {
			verdict.Reason = "combo already open"
		}
		// D-154 S3：disabled 触发的拒绝 stdout 1h 去重（combo 覆盖 reason 时不去重）。
		g.rejectLog(ctx, *intent, verdict.Reason, disabled && !comboAlreadyOpen)
		return GateResult{Status: "REJECTED", Reason: verdict.Reason}
	}

	// D-159 W6a S2：LIVE 人工开关——verdict.Pass=true 后才检查（disabled/dedup/daily_loss 拒绝时不查）。
	// is_real=true 账户 + LiveTrade=false → 拒绝开仓。demo（is_real=false）不受限制。
	// fail-closed：is_real 查询失败 → 拒绝。
	if !g.cfg.LiveTrade {
		if rv := g.liveGateCheck(ctx, *intent); !rv.Pass {
			g.rejectLog(ctx, *intent, rv.Reason, false)
			return GateResult{Status: "REJECTED", Reason: rv.Reason}
		}
	}

	// D-165 S1：swap 偏差拦截——同 venue 已有 OPEN 持仓的 broker 实际 swap vs facts 规格值偏差超阈值→拒绝。
	// 复用 ListActivePositions（line 91 已查）反查同 venue OPEN 持仓，取 DailySwapAccrued + LatestFacts 实时算偏差。
	// fail-closed：查询失败→拒绝。首次开仓无历史数据→放行（走 S3 零值校验）。
	if g.cfg.SwapDeviationGatePct > 0 {
		if rv := g.swapDeviationGateCheck(ctx, *intent, activePositions); !rv.Pass {
			g.rejectLog(ctx, *intent, rv.Reason, false)
			return GateResult{Status: "REJECTED", Reason: rv.Reason}
		}
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
		action := g.classifyFailure(*intent, msg)
		logEntry.Reason = action.String() + ": " + errclass.TruncateNote(msg, 120)
		if action == errclass.Halt {
			g.haltStrategy(ctx, *intent, logEntry.Reason)
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
