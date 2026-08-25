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
		// 净 funding 年化 ≤ FundingExitPct（默认 0）→ 平仓
		netFunding, ok := e.recalcNetFunding(ctx, p)
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
	// 48h 新鲜度守卫：对匹配到的 fact 检查（非列表首元素）。
	if time.Since(longFact.Ts) > 48*time.Hour || time.Since(shortFact.Ts) > 48*time.Hour {
		log.Printf("[autotrade] skip exit pos=%d: swap facts stale >48h", p.ID)
		return 0, false
	}
	return swapLong + swapShort, true
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
	// 48h 新鲜度守卫：对匹配到的 fact 检查（非列表首元素）。
	if time.Since(longFact.Ts) > 48*time.Hour || time.Since(shortFact.Ts) > 48*time.Hour {
		log.Printf("[autotrade] skip exit pos=%d: funding facts stale >48h", p.ID)
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
func (e *Engine) runStartupElimination(ctx context.Context) {
	strategies := []string{"CROSS_VENUE", "MEANREVERT", "SWAPARB", "CARRY", "FUNDING_HEDGE", "PERP_FUNDING"}
	for _, s := range strategies {
		stat, err := e.store.GetStrategyStat(ctx, s)
		if err != nil {
			continue
		}
		disable, reason := evaluateElimination(EliminationInput{
			TotalTrades: stat.TotalTrades,
			WinRate:     stat.WinRate,
			AvgPnl:      stat.AvgPnl,
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

// currentNetBps 实时计算持仓的当前净 bps（收敛型平仓用）。
func (e *Engine) currentNetBps(p *pgstore.Position) (float64, bool) {
	if e.quotes == nil {
		return p.ExpectedNetBps, true
	}
	canonical := canonicalFromOppID(p.OpportunityID)
	if canonical == "" {
		return p.ExpectedNetBps, true
	}
	legs, err := e.store.ListPositionLegs(context.Background(), p.ID)
	if err != nil {
		return 0, false
	}
	var buyLeg, sellLeg *pgstore.PositionLeg
	for i := range legs {
		if legs[i].Phase == "OPEN" {
			if legs[i].Side == "BUY" {
				buyLeg = &legs[i]
			} else {
				sellLeg = &legs[i]
			}
		}
	}
	if buyLeg == nil || sellLeg == nil {
		return 0, false
	}
	buyClose, ok1 := e.quotes.Latest(canonical, buyLeg.Venue)
	sellClose, ok2 := e.quotes.Latest(canonical, sellLeg.Venue)
	if !ok1 || !ok2 {
		return 0, false
	}
	spread := buyClose.Bid - sellClose.Ask
	if spread <= 0 {
		return 0, true
	}
	refPrice := buyClose.Ask
	if buyLeg.PriceActual != nil && *buyLeg.PriceActual > 0 {
		refPrice = *buyLeg.PriceActual
	}
	return spread / refPrice * 10000, true
}

// updateStrategyStat 已被 settlement.Settle 取代（D-087 §3.2，旧对象 no-op 缺陷根除）。
// 删除——不留死代码。淘汰判定经 settlement.Eliminator 注入。
