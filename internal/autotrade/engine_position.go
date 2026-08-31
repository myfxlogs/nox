package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/position"
	"arb/internal/store/pgstore"
)

// swapArbBasisExitBps SWAPARB/CARRY 基差止损门槛（D-108 A5，D-102 预设规则代码化）。
// 包级 var 便于测试覆写（marginReconnectBackoff 先例）。
var swapArbBasisExitBps = 4.0

// checkExitCondition 按 type 分派退出条件（D-072 §4.2）。
// 息差型判定走 position.IsCarryType 共用 helper（P3 单一真相：monitor.go 跳过收敛告警 + 此处退出分派同源）。
// 返回 (shouldClose, reason)。
func (e *Engine) checkExitCondition(ctx context.Context, p *pgstore.Position) (bool, string) {
	if !position.IsCarryType(p.Type) {
		// 收敛型（CROSS_VENUE/MEANREVERT 及未知类型）：currentNetBps ≤ CloseBpsThr
		currentBps, ok := e.currentNetBps(p)
		if !ok {
			return false, ""
		}
		if currentBps <= e.cfg.CloseBpsThr {
			return true, fmt.Sprintf("converged currentBps=%.2f <= %.1f", currentBps, e.cfg.CloseBpsThr)
		}
		return false, ""
	}
	// 息差型：按 swap/funding 子分派（IsCarryType 已判定为 4 类之一，此处只区分 swap vs funding）
	if p.Type == "FUNDING_HEDGE" || p.Type == "PERP_FUNDING" {
		// D-133 T1：PERP_FUNDING 走持有下限 + 连续去抖（修复 D-129 瞬时费率判平致命缺陷）；
		// FUNDING_HEDGE 保持旧语义（单次瞬时读数 ≤ 阈值即平，CFD 无结算窗概念）。
		netFunding, ok := e.recalcNetFunding(ctx, p)
		if p.Type == "PERP_FUNDING" {
			// D-133 T1：held = now − opened_at；opened_at nil（旧数据兜底）→ held=0（floor 未满，最安全）。
			var held time.Duration
			if p.OpenedAt != nil {
				held = time.Since(*p.OpenedAt)
			}
			e.fundingMu.Lock()
			curCount := e.fundingBreach[p.ID]
			shouldClose, newCount, reason := perpFundingExitDecision(ok, held, netFunding, e.cfg.FundingExitPct, e.cfg.PerpFundMinHold, e.cfg.PerpFundExitDebounce, curCount)
			if newCount == 0 {
				delete(e.fundingBreach, p.ID) // 计数清零/平仓 → 删除条目（防泄漏）
			} else {
				e.fundingBreach[p.ID] = newCount
			}
			e.fundingMu.Unlock()
			return shouldClose, reason
		}
		// FUNDING_HEDGE：旧语义——缺数据 fail-closed；netFunding ≤ 阈值即平。
		if !ok {
			return false, ""
		}
		if netFunding <= e.cfg.FundingExitPct {
			return true, fmt.Sprintf("net funding %.2f%% <= %.1f%%", netFunding, e.cfg.FundingExitPct)
		}
		return false, ""
	}
	// SWAPARB/CARRY：净 swap ≤ SwapArbExitBps（默认 0）→ 平仓
	netSwap, ok := e.recalcNetSwap(ctx, p)
	if !ok {
		return false, ""
	}
	if netSwap <= e.cfg.SwapArbExitBps {
		return true, fmt.Sprintf("net swap %.3f bps/day <= %.1f", netSwap, e.cfg.SwapArbExitBps)
	}
	// D-108 A5：基差止损——重算当前基差（两腿 venue 最新报价 cross-broker spread bps），
	// basisBps > swapArbBasisExitBps（默认 4）→ 平仓（D-102 预设规则代码化）。
	// 报价缺失/陈旧 → 不决策 return false（与 D-105 fail-closed 一致）。
	if basisBps, bok := e.currentBasisBps(p); bok {
		if basisBps > swapArbBasisExitBps {
			return true, fmt.Sprintf("basis loss limit %.1f bps > %.1f", basisBps, swapArbBasisExitBps)
		}
	}
	return false, ""
}

// recalcNetSwap 重算净 swap（D-072 §4.2，facts 驱动 + 48h 新鲜度守卫）。
// D-105 T1：venue 匹配加 found 标志——任一 venue 未匹配 fact → 缺数据不决策平仓（fail-closed，
// 修复 #264 误平：facts 缺失时静默取 0 → 0≤0 假信号平仓）。新鲜度守卫改为对匹配到的 fact 检查
// （旧值查 longFacts[0] 列表首元素，非该 venue 的 fact——同修）。
func (e *Engine) recalcNetSwap(ctx context.Context, p *pgstore.Position) (float64, bool) {
	canonical := canonicalFromOppID(p.OpportunityID)
	if canonical == "" {
		return 0, false
	}
	longFacts, err := e.store.LatestFacts(ctx, fact.KindSwapLong, "", canonical)
	if err != nil || len(longFacts) == 0 {
		return 0, false
	}
	shortFacts, err := e.store.LatestFacts(ctx, fact.KindSwapShort, "", canonical)
	if err != nil || len(shortFacts) == 0 {
		return 0, false
	}
	// 解析 legs 取 venue
	legs, err := e.store.ListPositionLegs(ctx, p.ID)
	if err != nil {
		return 0, false
	}
	var buyVenue, sellVenue string
	for i := range legs {
		if legs[i].Phase == "OPEN" {
			if legs[i].Side == "BUY" {
				buyVenue = legs[i].Venue
			} else {
				sellVenue = legs[i].Venue
			}
		}
	}
	if buyVenue == "" || sellVenue == "" {
		return 0, false
	}
	buyBare := strings.Split(buyVenue, ":")[0]
	sellBare := strings.Split(sellVenue, ":")[0]
	// D-105 T1：venue 匹配加 found 标志，缺数据不决策平仓。
	var swapLong, swapShort float64
	var longFact, shortFact *fact.Fact
	for i := range longFacts {
		if longFacts[i].Venue == buyBare {
			swapLong = longFacts[i].Value
			longFact = &longFacts[i]
		}
	}
	for i := range shortFacts {
		if shortFacts[i].Venue == sellBare {
			swapShort = shortFacts[i].Value
			shortFact = &shortFacts[i]
		}
	}
	if longFact == nil {
		log.Printf("[autotrade] exit pos=%d: swap fact missing venue=%s", p.ID, buyBare)
		return 0, false
	}
	if shortFact == nil {
		log.Printf("[autotrade] exit pos=%d: swap fact missing venue=%s", p.ID, sellBare)
		return 0, false
	}
	// D-159 W4a R7：TTL 统一用 cfg.SwapFactMaxAge（env 单源，默认 2h），与 discoverSwapCombos/swapledger 同源。
	maxAge := e.cfg.SwapFactMaxAge
	if maxAge > 0 && (time.Since(longFact.Ts) > maxAge || time.Since(shortFact.Ts) > maxAge) {
		log.Printf("[autotrade] skip exit pos=%d: swap facts stale >%v", p.ID, maxAge)
		return 0, false
	}
	factsNetSwap := swapLong + swapShort
	// D-165 S2：broker 实际 swap 对拍——偏差超阈值用实际值触发退出。
	// 复用 DailySwapAccrued 取 broker 实际日增量（USD），换算 bps 后 vs facts 规格值算偏差。
	// 偏差 > SwapDeviationExitPct → 改用 broker 实际值（bps）返回（修复"规格声明 0 但实际负→退出路径不触发"）。
	// 无快照/ok=false → 用 facts 规格值（回退原行为，不阻塞退出决策）。
	if e.cfg.SwapDeviationExitPct > 0 {
		if actualBps, ok := e.brokerActualNetSwapBps(ctx, p, legs, buyBare, sellBare, factsNetSwap); ok {
			if deviationPct := swapDeviationBetween(factsNetSwap, actualBps); deviationPct > e.cfg.SwapDeviationExitPct {
				log.Printf("[autotrade] exit pos=%d: swap deviation %.1f%% > %.1f%%, use broker actual %.4f bps vs facts %.4f bps",
					p.ID, deviationPct, e.cfg.SwapDeviationExitPct, actualBps, factsNetSwap)
				return actualBps, true
			}
		}
	}
	return factsNetSwap, true
}

// brokerActualNetSwapBps D-165 S2：取 broker 实际净 swap（USD/日）换算 bps。
// 复用 recalcNetSwap 已查的 legs + buyBare/sellBare。DailySwapAccrued 取两腿当日增量。
// notional 从 legs qty×price_actual 算（无 CS 简化，CEX 腿 CS=1，MT 腿 price_actual 已含 CS 折算）。
// 任一腿无快照/ok=false → 返回 (0, false)（回退 facts）。
// factsNetSwap 用于 notional 缺失时回退（无法换算→false）。
func (e *Engine) brokerActualNetSwapBps(ctx context.Context, p *pgstore.Position, legs []pgstore.PositionLeg, buyBare, sellBare string, factsNetSwap float64) (float64, bool) {
	now := time.Now().UTC()
	var buyActualUSD, sellActualUSD float64
	var buyNotional, sellNotional float64
	buyOK, sellOK := false, false
	for _, leg := range legs {
		if leg.Phase != "OPEN" {
			continue
		}
		legBare := strings.Split(leg.Venue, ":")[0]
		notional := leg.Qty
		if leg.PriceActual != nil {
			notional *= *leg.PriceActual
		}
		if leg.Side == "BUY" && legBare == buyBare {
			usd, ok, err := e.store.DailySwapAccrued(ctx, p.ID, buyBare, now)
			if err != nil || !ok {
				return 0, false // 任一腿无快照→回退 facts
			}
			buyActualUSD = usd
			buyNotional = notional
			buyOK = true
		} else if leg.Side != "BUY" && legBare == sellBare {
			usd, ok, err := e.store.DailySwapAccrued(ctx, p.ID, sellBare, now)
			if err != nil || !ok {
				return 0, false
			}
			sellActualUSD = usd
			sellNotional = notional
			sellOK = true
		}
	}
	if !buyOK || !sellOK {
		return 0, false
	}
	// 净 swap USD/日 = buyActual + sellActual（BUY 腿通常负，SELL 腿通常正，净值=两腿之和）
	netActualUSD := buyActualUSD + sellActualUSD
	// 换算 bps：用两腿 notional 平均值（简化，对冲两腿名义相近）
	avgNotional := (buyNotional + sellNotional) / 2
	if avgNotional <= 0 {
		return 0, false // 无法换算→回退 facts
	}
	return netActualUSD / avgNotional * 10000, true
}

// recalcNetFunding 重算净 funding 年化（D-072 §4.2，facts 驱动 + 48h 新鲜度守卫）。
// D-105 T1：同 recalcNetSwap fail-closed 修复——venue 匹配加 found 标志，任一 venue 未匹配
// → 缺数据不决策平仓；新鲜度守卫改为对匹配到的 fact 检查（旧值查列表首元素同修）。
func (e *Engine) recalcNetFunding(ctx context.Context, p *pgstore.Position) (float64, bool) {
	canonical := canonicalFromOppID(p.OpportunityID)
	if canonical == "" {
		return 0, false
	}
	fundingFacts, err := e.store.LatestFacts(ctx, fact.KindFunding, "", canonical)
	if err != nil || len(fundingFacts) == 0 {
		return 0, false
	}
	legs, err := e.store.ListPositionLegs(ctx, p.ID)
	if err != nil {
		return 0, false
	}
	var buyVenue, sellVenue string
	for i := range legs {
		if legs[i].Phase == "OPEN" {
			if legs[i].Side == "BUY" {
				buyVenue = legs[i].Venue
			} else {
				sellVenue = legs[i].Venue
			}
		}
	}
	if buyVenue == "" || sellVenue == "" {
		return 0, false
	}
	buyBare := strings.Split(buyVenue, ":")[0]
	sellBare := strings.Split(sellVenue, ":")[0]
	// D-105 T1：venue 匹配加 found 标志，缺数据不决策平仓。
	var longFunding, shortFunding float64
	var longFact, shortFact *fact.Fact
	for i := range fundingFacts {
		if fundingFacts[i].Venue == buyBare {
			longFunding = fundingFacts[i].Value
			longFact = &fundingFacts[i]
		}
		if fundingFacts[i].Venue == sellBare {
			shortFunding = fundingFacts[i].Value
			shortFact = &fundingFacts[i]
		}
	}
	if longFact == nil {
		log.Printf("[autotrade] exit pos=%d: funding fact missing venue=%s", p.ID, buyBare)
		return 0, false
	}
	if shortFact == nil {
		log.Printf("[autotrade] exit pos=%d: funding fact missing venue=%s", p.ID, sellBare)
		return 0, false
	}
	// D-159 W4a R7：TTL 统一用 cfg.SwapFactMaxAge（env 单源，默认 2h），与 discoverSwapCombos/recalcNetSwap 同源。
	maxAge := e.cfg.SwapFactMaxAge
	if maxAge > 0 && (time.Since(longFact.Ts) > maxAge || time.Since(shortFact.Ts) > maxAge) {
		log.Printf("[autotrade] skip exit pos=%d: funding facts stale >%v", p.ID, maxAge)
		return 0, false
	}
	return longFunding - shortFunding, true
}

// canonicalFromOppID 从 opportunity_id 解析 canonical（格式 PREFIX:SYMBOL:...）。
func canonicalFromOppID(oppID string) string {
	parts := strings.Split(oppID, ":")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

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
		_ = e.store.SetAutotradeState(ctx, next, reason, haltedOn)
		log.Printf("[autotrade] state %s → %s: %s", stateRow.State, next, reason)
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
