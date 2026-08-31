package autotrade

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"arb/internal/combo"
	"arb/internal/errclass"
	"arb/internal/fact"
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
	// D-154 S3：disabled 拒绝 stdout 1h 去重（复用 D-144 ② per-key 1h 模式）。
	// DB 落库保留全量（审计完整性不动），只压 stdout。内存，重启重置。
	lastDisabledLog   map[string]time.Time
	lastDisabledLogMu sync.Mutex
}

// NewGate 创建门禁。
func NewGate(store *pgstore.Store, cfg Config) *Gate {
	return &Gate{
		store:           store,
		cfg:             cfg,
		lastDisabledLog: make(map[string]time.Time),
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
		g.rejectLog(ctx, intent, fmt.Sprintf("get state: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "get autotrade state failed"}
	}
	state := stateRow.State

	// 检查 2: 策略禁用（fail-closed：查询失败 = 拒绝）
	disabled, err := g.store.IsStrategyDisabled(ctx, intent.Strategy)
	if err != nil {
		g.rejectLog(ctx, intent, fmt.Sprintf("check strategy disabled: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "strategy disabled check failed"}
	}

	// 检查 3: 日亏损（fail-closed：查询失败 = 拒绝）
	dailyPnl, err := g.store.DailyRealizedPnl(ctx)
	if err != nil {
		g.rejectLog(ctx, intent, fmt.Sprintf("daily pnl: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "daily pnl check failed"}
	}

	// 检查 4+6: 活跃持仓数 + 同组合防重（D-080 R1：DB 实时派生，一条 SQL 替代 CountOpenPositions + 内存 openCombos）
	// fail-closed：查询失败 = 拒绝（对齐检查 2-3 风格）
	activePositions, err := g.store.ListActivePositions(ctx)
	if err != nil {
		g.rejectLog(ctx, intent, fmt.Sprintf("list active positions: %v", err), false)
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

	// D-159 W1a R1：精确组合门禁（SWAPARB 专用，gateDecision 后、Execute 前拦截）。
	// gateDecision 已过（state=RUNNING、未禁用、未超仓、未超 notional、未 dedup）才进入此检查。
	// 非 RUNNING 状态（CLOSE_ONLY 等）已在 gateDecision 拒绝，不会到此。
	if intent.Strategy == "SWAPARB" {
		if cv := comboAllowVerdict(g.cfg.SwapArbAllowCombos, g.cfg.MaxActiveCombos, intent, activePositions); !cv.Pass {
			g.rejectLog(ctx, intent, cv.Reason, false)
			return GateResult{Status: "REJECTED", Reason: cv.Reason}
		}
	}

	// D-159 W1a I6：environment/is_real 一致性检查（所有策略，gateDecision 后）。
	// 校验 venue 的 broker_accounts.is_real 与 g.cfg.Environment 一致：
	//   is_real=true ↔ environment='live' / is_real=false ↔ environment='demo'
	//   is_real=nil（未知）→ fail-closed 拒绝（不允许未知环境开仓）。
	// Environment 空 = 未配置（不检查，回退旧行为）。
	if g.cfg.Environment != "" {
		if rv := g.environmentIsRealCheck(ctx, intent); !rv.Pass {
			g.rejectLog(ctx, intent, rv.Reason, false)
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
			_ = g.store.SetAutotradeState(ctx, StateDailyLossHalt, verdict.Reason, &haltedOn)
		}
		// D-080 S1：同组合防重拒绝原因覆盖（gateDecision 返回通用 "duplicate dedup key"）
		if comboAlreadyOpen {
			verdict.Reason = "combo already open"
		}
		// D-154 S3：disabled 触发的拒绝 stdout 1h 去重（combo 覆盖 reason 时不去重）。
		g.rejectLog(ctx, intent, verdict.Reason, disabled && !comboAlreadyOpen)
		return GateResult{Status: "REJECTED", Reason: verdict.Reason}
	}

	// D-159 W6a S2：LIVE 人工开关——verdict.Pass=true 后才检查（disabled/dedup/daily_loss 拒绝时不查）。
	// is_real=true 账户 + LiveTrade=false → 拒绝开仓。demo（is_real=false）不受限制。
	// fail-closed：is_real 查询失败 → 拒绝。
	if !g.cfg.LiveTrade {
		if rv := g.liveGateCheck(ctx, intent); !rv.Pass {
			g.rejectLog(ctx, intent, rv.Reason, false)
			return GateResult{Status: "REJECTED", Reason: rv.Reason}
		}
	}

	// D-165 S1：swap 偏差拦截——同 venue 已有 OPEN 持仓的 broker 实际 swap vs facts 规格值偏差超阈值→拒绝。
	// 复用 ListActivePositions（line 91 已查）反查同 venue OPEN 持仓，取 DailySwapAccrued + LatestFacts 实时算偏差。
	// fail-closed：查询失败→拒绝。首次开仓无历史数据→放行（走 S3 零值校验）。
	if g.cfg.SwapDeviationGatePct > 0 {
		if rv := g.swapDeviationGateCheck(ctx, intent, activePositions); !rv.Pass {
			g.rejectLog(ctx, intent, rv.Reason, false)
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
// D-154 S3：disabled=true 时 stdout 1h 去重（per-strategy），DB 落库保留全量（审计完整性不动）。
func (g *Gate) rejectLog(ctx context.Context, intent TradeIntent, reason string, disabled bool) {
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
	if disabled {
		// stdout 1h 去重（per-strategy）；DB 已全量落库
		g.lastDisabledLogMu.Lock()
		last, ok := g.lastDisabledLog[intent.Strategy]
		if ok && time.Since(last) < time.Hour {
			g.lastDisabledLogMu.Unlock()
			return
		}
		g.lastDisabledLog[intent.Strategy] = time.Now()
		g.lastDisabledLogMu.Unlock()
	}
	log.Printf("[autotrade] gate REJECT %s: %s", intent.DedupKey, reason)
}

// environmentIsRealCheck D-159 W1a I6：environment/is_real 一致性检查。
// 校验 intent 双腿 venue 的 broker_accounts.is_real 与 g.cfg.Environment 一致：
//   - is_real=true ↔ environment='live'
//   - is_real=false ↔ environment='demo'
//   - is_real=nil（未知）→ fail-closed 拒绝
//   - venue 不存在 → fail-closed 拒绝
//
// venueKey 格式 "account:mt5"，venue=account（broker_accounts.venue）。
func (g *Gate) environmentIsRealCheck(ctx context.Context, intent TradeIntent) GateVerdict {
	wantLive := g.cfg.Environment == "live"
	for _, venueKey := range []string{intent.VenueBuy, intent.VenueSell} {
		venue, _, _ := strings.Cut(venueKey, ":")
		if venue == "" {
			return GateVerdict{false, fmt.Sprintf("environment check: empty venue in %s", venueKey)}
		}
		acct, err := g.store.GetBrokerAccount(ctx, venue)
		if err != nil {
			return GateVerdict{false, fmt.Sprintf("environment check: venue %s not found (%v)", venue, err)}
		}
		if acct.IsReal == nil {
			return GateVerdict{false, fmt.Sprintf("environment check: venue %s is_real unknown (null)", venue)}
		}
		isLive := *acct.IsReal
		if isLive != wantLive {
			return GateVerdict{false, fmt.Sprintf("environment check: venue %s is_real=%v != environment=%s", venue, isLive, g.cfg.Environment)}
		}
	}
	return GateVerdict{Pass: true}
}

// liveGateCheck D-159 W6a S2：LIVE 人工开关检查。
// LiveTrade=false 时，任一腿 is_real=true → 拒绝（实盘账户不允许开仓）。
// is_real=false（demo）→ 放行。is_real=nil（未知）→ fail-closed 拒绝。
// venue 不存在 → fail-closed 拒绝。
func (g *Gate) liveGateCheck(ctx context.Context, intent TradeIntent) GateVerdict {
	for _, venueKey := range []string{intent.VenueBuy, intent.VenueSell} {
		venue, _, _ := strings.Cut(venueKey, ":")
		if venue == "" {
			return GateVerdict{false, fmt.Sprintf("live gate: empty venue in %s", venueKey)}
		}
		acct, err := g.store.GetBrokerAccount(ctx, venue)
		if err != nil {
			return GateVerdict{false, fmt.Sprintf("live gate: venue %s not found (%v)", venue, err)}
		}
		if acct.IsReal == nil {
			return GateVerdict{false, fmt.Sprintf("live gate: venue %s is_real unknown (null)", venue)}
		}
		if *acct.IsReal {
			return GateVerdict{false, fmt.Sprintf("live trade disabled (ARB_LIVE_TRADE=false, venue=%s is_real=true)", venue)}
		}
	}
	return GateVerdict{Pass: true}
}

// swapDeviationGateCheck D-165 S1：swap 偏差拦截——同 venue 已有 OPEN 持仓的 broker 实际 swap
// vs facts 规格值偏差超阈值→拒绝新开仓。
// 逻辑：
//   - 遍历 activePositions，对每个 OPEN 持仓 ListPositionLegs 取 OPEN 腿 venue。
//   - 腿 venue 与 intent.VenueBuy/VenueSell 的 bare venue 匹配→该持仓是"同 venue 历史持仓"。
//   - 对匹配持仓的 venue，DailySwapAccrued 取 broker 实际日增量（USD），LatestFacts 取规格值（bps）。
//   - facts 规格值换算 USD/日 = factsBps × notional / 10000（notional 从 legs qty×price×CS 算）。
//   - 用 swapDeviationBetween 纯函数算偏差率，超阈值→拒绝。
//   - 无匹配持仓/无 DailySwapAccrued/无 facts→放行（首次开仓走 S3）。
//   - 查询失败→fail-closed 拒绝。
func (g *Gate) swapDeviationGateCheck(ctx context.Context, intent TradeIntent, activePositions []pgstore.Position) GateVerdict {
	intentBuyBare, _, _ := strings.Cut(intent.VenueBuy, ":")
	intentSellBare, _, _ := strings.Cut(intent.VenueSell, ":")
	canonical := intent.Canonical

	// 取 facts 规格值（同 recalcNetSwap 逻辑）
	longFacts, err := g.store.LatestFacts(ctx, fact.KindSwapLong, "", canonical)
	if err != nil {
		return GateVerdict{false, fmt.Sprintf("swap deviation gate: long facts query failed (%v)", err)}
	}
	shortFacts, err := g.store.LatestFacts(ctx, fact.KindSwapShort, "", canonical)
	if err != nil {
		return GateVerdict{false, fmt.Sprintf("swap deviation gate: short facts query failed (%v)", err)}
	}

	for _, p := range activePositions {
		legs, err := g.store.ListPositionLegs(ctx, p.ID)
		if err != nil {
			return GateVerdict{false, fmt.Sprintf("swap deviation gate: list legs pos=%d failed (%v)", p.ID, err)}
		}
		for _, leg := range legs {
			if leg.Phase != "OPEN" {
				continue
			}
			legBare, _, _ := strings.Cut(leg.Venue, ":")
			// 只检查与 intent venue 匹配的腿
			if legBare != intentBuyBare && legBare != intentSellBare {
				continue
			}
			// 取 broker 实际 swap 日增量
			actualUSD, ok, err := g.store.DailySwapAccrued(ctx, p.ID, legBare, time.Now().UTC())
			if err != nil {
				return GateVerdict{false, fmt.Sprintf("swap deviation gate: daily swap accrued pos=%d venue=%s failed (%v)", p.ID, legBare, err)}
			}
			if !ok || actualUSD == 0 {
				continue // 无快照或零增量→跳过（走 S3）
			}
			// 取 facts 规格值（bps）
			var factsBps float64
			if leg.Side == "BUY" {
				for _, f := range longFacts {
					if f.Venue == legBare {
						factsBps = f.Value
						break
					}
				}
			} else {
				for _, f := range shortFacts {
					if f.Venue == legBare {
						factsBps = f.Value
						break
					}
				}
			}
			// facts 换算 USD/日 = bps × notional / 10000
			notional := leg.Qty
			if leg.PriceActual != nil {
				notional *= *leg.PriceActual
			}
			if notional <= 0 {
				continue // 无法算 notional→跳过
			}
			factsUSD := factsBps * notional / 10000
			// 偏差率
			deviationPct := swapDeviationBetween(factsUSD, actualUSD)
			if deviationPct > g.cfg.SwapDeviationGatePct {
				return GateVerdict{false, fmt.Sprintf("swap deviation gate: venue=%s deviation=%.1f%% > %.1f%% (broker actual=$%.4f/day vs facts spec=$%.4f/day)",
					legBare, deviationPct, g.cfg.SwapDeviationGatePct, actualUSD, factsUSD)}
			}
		}
	}
	return GateVerdict{Pass: true}
}

// swapDeviationBetween D-165 S1/S2：偏差率纯函数（复用 swapledger.reconcileLeg 公式，但独立实现避免跨包依赖）。
// 公式：|facts - actual| / max(|facts|, |actual|, 0.01) × 100。
// 两者为 0→偏差 0（一致）；任一为 0 另一非 0→偏差 100%。
func swapDeviationBetween(facts, actual float64) float64 {
	diff := facts - actual
	if diff < 0 {
		diff = -diff
	}
	maxVal := math.Abs(facts)
	if math.Abs(actual) > maxVal {
		maxVal = math.Abs(actual)
	}
	if maxVal < 0.01 {
		maxVal = 0.01
	}
	return diff / maxVal * 100
}
