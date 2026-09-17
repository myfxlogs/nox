package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"arb/internal/combo"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
	"arb/internal/usdnotional"
)

// ExecuteOpportunityInternal 执行机会的核心逻辑（D-070 抽取，供 RPC 和 autotrade engine 复用）。
// 返回 status（EXECUTED/FAILED）、message、positionID（失败时为0）。
func (s *Service) ExecuteOpportunityInternal(ctx context.Context, oppID, requestID string, qty float64) (status, message string, positionID int64) {
	if s.pipeline == nil {
		return "FAILED", "executor not wired", 0
	}
	if qty <= 0 {
		return "FAILED", "invalid quantity", 0
	}
	// D-195-A：统一执行模式检查（手动入口同源，首版只用 global scope）
	if s.gateChecker != nil {
		mode, allowed, cpReason, cpErr := s.gateChecker.CheckGate(ctx)
		if cpErr != nil || !allowed {
			return "FAILED", fmt.Sprintf("execution mode blocked: %s (mode=%s)", cpReason, mode), 0
		}
	}
	opps, err := s.store.ListOpportunities(ctx, "", 200, 0)
	if err != nil {
		return "FAILED", fmt.Sprintf("list opportunities: %v", err), 0
	}
	var found *pgstore.Opportunity
	for i := range opps {
		if opps[i].ID == oppID {
			found = &opps[i]
			break
		}
	}
	if found == nil {
		return "FAILED", "opportunity not found or expired", 0
	}
	// D-159 W6a S5：手动 RPC 不绕 CLOSE_ONLY——非 RUNNING 拒绝开仓（fail-closed）。
	state, err := s.checkAutotradeState(ctx)
	if err != nil {
		return "FAILED", fmt.Sprintf("autotrade state: %v (fail-closed)", err), 0
	}
	if state != "RUNNING" {
		return "FAILED", fmt.Sprintf("autotrade not RUNNING (state=%s), open rejected", state), 0
	}
	// D-311 S4/R3：跨 canonical 卖腿——CanonicalSell nil/空回退 Canonical
	//（Revalidate/freezeGuard/combo 门禁同源，P3 单一真相）。
	sellCanonical := found.Canonical
	if found.CanonicalSell != nil && *found.CanonicalSell != "" {
		sellCanonical = *found.CanonicalSell
	}
	if !s.pipeline.Revalidate(found.Canonical, sellCanonical, found.VenueBuy, found.VenueSell, found.Type, found.GrossBps) {
		return "FAILED", "quote stale or net benefit degraded beyond threshold", 0
	}
	if err := s.checkTradability(found); err != nil {
		return "FAILED", err.Error(), 0
	}
	legs, err := s.resolveLegs(found, qty)
	if err != nil {
		return "FAILED", err.Error(), 0
	}
	if err := s.validateLegQtys(legs); err != nil { // D-127 P3a：qty 预检 fail-closed
		log.Printf("[exec] %v", err)
		return "FAILED", err.Error(), 0
	}
	// freezeGuard 双腿各自 canonical——卖腿 tick 在 canonical_sell 命名空间下（sellCanonical 前置 :56 已算）。
	if err := s.checkFreezeGuard(found.Canonical, sellCanonical, legs); err != nil { // D-155：冻结腿预检 fail-closed
		log.Printf("[exec] %v", err)
		return "FAILED", err.Error(), 0
	}
	// D-159 W1a R1：SWAPARB 精确组合门禁（手动入口同源，pipeline.Execute 前拦截）。
	// swapArbComboAllow 非 nil 时检查；nil = 未接线（gate 层兜底）。
	if found.Type == "SWAPARB" && len(s.swapArbComboAllow) > 0 {
		if exec.MatchComboAllow(s.swapArbComboAllow, found.Canonical, found.VenueBuy, found.VenueSell) == nil {
			return "FAILED", fmt.Sprintf("combo not in allowlist: %s %s×%s", found.Canonical, found.VenueBuy, found.VenueSell), 0
		}
	}
	// D-311 S6：XASSET_CARRY 组合门禁（手动入口同源）——零信任：空 allowlist 全拒，
	// 命中才放行（区别于 SWAPARB 的 nil=未接线；pipeline 层另有名义额/gross 校验）。
	if found.Type == "XASSET_CARRY" {
		if exec.MatchXAssetComboAllow(s.xassetComboAllow, found.Canonical, sellCanonical, found.VenueBuy, found.VenueSell) == nil {
			return "FAILED", fmt.Sprintf("combo not in xasset allowlist: %s/%s %s×%s", found.Canonical, sellCanonical, found.VenueBuy, found.VenueSell), 0
		}
	}
	// D-159 W1a I6：environment/is_real 一致性检查（手动入口同源，pipeline.Execute 前拦截）。
	if s.environment != "" {
		if msg := s.checkEnvironmentIsReal(ctx, found.VenueBuy, found.VenueSell); msg != "" {
			return "FAILED", msg, 0
		}
	}
	// D-159 W6a S2：LIVE 人工开关（手动入口同源，pipeline.Execute 前拦截）。
	if !s.liveTrade {
		if msg := s.checkLiveGate(ctx, found.VenueBuy, found.VenueSell); msg != "" {
			return "FAILED", msg, 0
		}
	}
	// D-182 S4：swap 偏差拦截（手动入口同源，pipeline.Execute 前拦截）。
	if msg := s.checkSwapDeviationGate(ctx, found.Canonical, found.VenueBuy, found.VenueSell); msg != "" {
		return "FAILED", msg, 0
	}
	// D-328 S4：carry 入口 funding regime 持久性闸（XASSET 唯一漏斗；autotrade intent.Execute
	// 亦经此函数=S3 闸失效的兜底防线）。失败 FAILED 带 reason。
	if found.Type == "XASSET_CARRY" {
		if ok, reason := exec.CarryFundingRegimeCheck(ctx, s.store, found.VenueBuy, found.VenueSell, found.Canonical, sellCanonical, time.Now(), s.carryRegime); !ok {
			return "FAILED", "funding regime 未持续: " + reason, 0
		}
	}
	result, err := s.pipeline.Execute(ctx, oppID, requestID, found.Type, legs)
	if err != nil {
		if errors.Is(err, exec.ErrMaxOpenPositions) || errors.Is(err, exec.ErrDuplicateExecution) ||
			errors.Is(err, exec.ErrEmptyPositionType) || errors.Is(err, exec.ErrEmptyLegs) {
			return "FAILED", err.Error(), 0
		}
		return "FAILED", fmt.Sprintf("execute: %v", err), 0
	}
	// D-070：回填 position 元数据（expected_net_bps/expected_annual_pct；type 已在创建时写入，修复⑭ S4）
	// D-087：typed 化（metaForType 按型构造 PositionMeta）
	if result.PositionID > 0 {
		_ = s.store.UpdatePositionMeta(ctx, result.PositionID, found.Type, metaForType(found.Type, found.NetBps))
		// D-327 S1：XASSET_CARRY 补写成交价基准——metaForType 不产 EntrySpreadBps 且
		// UpdatePositionMeta 无条件覆写该列，必须在 meta 之后补；IS NULL 幂等（已有不覆写）。
		if found.Type == "XASSET_CARRY" {
			_ = s.store.EnsureEntrySpreadFromLegs(ctx, result.PositionID)
		}
	}
	return result.Status, "", result.PositionID
}

// ExecuteMeanRevert D-070 均值回归直接执行（绕过 detector，autotrade engine 专用）。
// 直接接收 canonical + venueBuy + venueSell + qty，构造 LegSpec 并执行。
// maxNotional: CFD 名义额上限（0=用默认$10K）。
// 返回 status、message、positionID。
func (s *Service) ExecuteMeanRevert(ctx context.Context, canonical, venueBuy, venueSell, requestID string, qty float64, openBps float64) (status, message string, positionID int64) {
	return s.ExecuteMeanRevertWithCap(ctx, canonical, venueBuy, venueSell, requestID, qty, openBps, 10000.0, "MEANREVERT")
}

// ExecuteMeanRevertWithCap 带 notional cap 参数的均值回归执行（swap 套利可用更大 cap）。
// strategyType: 写入 positions.type（D-072 §4.1，SWAPARB/MEANREVERT）。
func (s *Service) ExecuteMeanRevertWithCap(ctx context.Context, canonical, venueBuy, venueSell, requestID string, qty float64, openBps float64, maxNotional float64, strategyType string) (status, message string, positionID int64) {
	if s.pipeline == nil {
		return "FAILED", "executor not wired", 0
	}
	if qty <= 0 {
		return "FAILED", "invalid quantity", 0
	}
	// D-195-A：统一执行模式检查（手动入口同源，首版只用 global scope）
	if s.gateChecker != nil {
		mode, allowed, cpReason, cpErr := s.gateChecker.CheckGate(ctx)
		if cpErr != nil || !allowed {
			return "FAILED", fmt.Sprintf("execution mode blocked: %s (mode=%s)", cpReason, mode), 0
		}
	}
	// D-159 W6a S5：手动 RPC 不绕 CLOSE_ONLY——非 RUNNING 拒绝开仓（fail-closed）。
	state, err := s.checkAutotradeState(ctx)
	if err != nil {
		return "FAILED", fmt.Sprintf("autotrade state: %v (fail-closed)", err), 0
	}
	if state != "RUNNING" {
		return "FAILED", fmt.Sprintf("autotrade not RUNNING (state=%s), open rejected", state), 0
	}
	if maxNotional <= 0 {
		maxNotional = 10000.0
	}
	qbuy, okBuy := s.qbus.Latest(canonical, venueBuy)
	qsell, okSell := s.qbus.Latest(canonical, venueSell)
	if !okBuy || !okSell {
		return "FAILED", "buy/sell leg quote unavailable", 0
	}
	csBuy, err := s.legContractSizeOf(venueBuy, qbuy.VenueSymbol)
	if err != nil {
		return "FAILED", fmt.Sprintf("buy CS: %v", err), 0
	}
	csSell, err := s.legContractSizeOf(venueSell, qsell.VenueSymbol)
	if err != nil {
		return "FAILED", fmt.Sprintf("sell CS: %v", err), 0
	}
	sellQty := math.Round(qty*csBuy/csSell*100) / 100
	qty = s.floorToStepQty(venueBuy, qbuy.VenueSymbol, qty)
	sellQty = s.floorToStepQty(venueSell, qsell.VenueSymbol, sellQty)
	// CFD notional cap（D-190：两处 cap 检查改 usdnotional.PerLot，USD 统一换算）
	if isMTVenue(venueBuy) {
		perLot, ok := usdnotional.PerLot(canonical, qbuy.Ask, csBuy, s.qbus)
		if !ok {
			return "FAILED", "buy leg usd per lot unavailable (fail-closed)", 0
		}
		notional := qty * perLot
		if notional > maxNotional {
			qty = s.floorToStepQtyMin(venueBuy, qbuy.VenueSymbol)
		}
	}
	if isMTVenue(venueSell) {
		perLot, ok := usdnotional.PerLot(canonical, qsell.Bid, csSell, s.qbus)
		if !ok {
			return "FAILED", "sell leg usd per lot unavailable (fail-closed)", 0
		}
		notional := sellQty * perLot
		if notional > maxNotional {
			sellQty = s.floorToStepQtyMin(venueSell, qsell.VenueSymbol)
		}
	}
	// imbalance 检查
	ref := qbuy.Ask
	notionalBuy := qty * csBuy * ref
	notionalSell := sellQty * csSell * ref
	maxImb := s.maxLegImbalance
	if maxImb <= 0 {
		maxImb = 2.0
	}
	imb := math.Abs(notionalSell-notionalBuy) / notionalBuy
	if notionalBuy <= 0 || imb > maxImb/100 {
		return "FAILED", fmt.Sprintf("leg notional imbalance: buy $%.2f vs sell $%.2f (%.2f%% > %.1f%%)",
			notionalBuy, notionalSell, imb*100, maxImb), 0
	}
	if err := s.checkLegVolume(venueBuy, qbuy.VenueSymbol, qty); err != nil {
		return "FAILED", err.Error(), 0
	}
	if err := s.checkLegVolume(venueSell, qsell.VenueSymbol, sellQty); err != nil {
		return "FAILED", err.Error(), 0
	}
	oppID := combo.Key("MEANREVERT", canonical, venueBuy, venueSell)
	legs := []exec.LegSpec{
		{VenueKey: venueBuy, VenueSymbol: qbuy.VenueSymbol, Side: "BUY", Qty: qty, PriceExpected: qbuy.Ask},
		{VenueKey: venueSell, VenueSymbol: qsell.VenueSymbol, Side: "SELL", Qty: sellQty, PriceExpected: qsell.Bid},
	}
	if err := s.validateLegQtys(legs); err != nil { // D-127 P3a：qty 预检 fail-closed
		log.Printf("[exec] %v", err)
		return "FAILED", err.Error(), 0
	}
	if err := s.checkFreezeGuard(canonical, canonical, legs); err != nil { // D-155：冻结腿预检 fail-closed（D-311：单 canonical 路径双腿同键）
		log.Printf("[exec] %v", err)
		return "FAILED", err.Error(), 0
	}
	// D-159 W1a R1：SWAPARB 精确组合门禁（手动入口同源，pipeline.Execute 前拦截）。
	if strategyType == "SWAPARB" && len(s.swapArbComboAllow) > 0 {
		if exec.MatchComboAllow(s.swapArbComboAllow, canonical, venueBuy, venueSell) == nil {
			return "FAILED", fmt.Sprintf("combo not in allowlist: %s %s×%s", canonical, venueBuy, venueSell), 0
		}
	}
	// D-311 S6：XASSET_CARRY 组合门禁（手动入口同源）——零信任：空 allowlist 全拒。
	// 本路径无 opportunities 行可查 canonical_sell → 双腿同 canonical 兜底（与 pipeline 门禁同源）。
	if strategyType == "XASSET_CARRY" {
		if exec.MatchXAssetComboAllow(s.xassetComboAllow, canonical, canonical, venueBuy, venueSell) == nil {
			return "FAILED", fmt.Sprintf("combo not in xasset allowlist: %s %s×%s", canonical, venueBuy, venueSell), 0
		}
	}
	// D-159 W1a I6：environment/is_real 一致性检查（手动入口同源，pipeline.Execute 前拦截）。
	if s.environment != "" {
		if msg := s.checkEnvironmentIsReal(ctx, venueBuy, venueSell); msg != "" {
			return "FAILED", msg, 0
		}
	}
	// D-159 W6a S2：LIVE 人工开关（手动入口同源，pipeline.Execute 前拦截）。
	if !s.liveTrade {
		if msg := s.checkLiveGate(ctx, venueBuy, venueSell); msg != "" {
			return "FAILED", msg, 0
		}
	}
	// D-182 S4：swap 偏差拦截（手动入口同源，pipeline.Execute 前拦截）。
	if msg := s.checkSwapDeviationGate(ctx, canonical, venueBuy, venueSell); msg != "" {
		return "FAILED", msg, 0
	}
	result, err := s.pipeline.Execute(ctx, oppID, requestID, strategyType, legs)
	if err != nil {
		if errors.Is(err, exec.ErrMaxOpenPositions) || errors.Is(err, exec.ErrDuplicateExecution) ||
			errors.Is(err, exec.ErrEmptyPositionType) || errors.Is(err, exec.ErrEmptyLegs) {
			return "FAILED", err.Error(), 0
		}
		return "FAILED", fmt.Sprintf("execute: %v", err), 0
	}
	if result.PositionID > 0 {
		_ = s.store.UpdatePositionMeta(ctx, result.PositionID, strategyType, metaForType(strategyType, openBps))
		// D-327 S1：XASSET_CARRY 补写成交价基准（同 :128 写点——meta 覆写后补，IS NULL 幂等）。
		if strategyType == "XASSET_CARRY" {
			_ = s.store.EnsureEntrySpreadFromLegs(ctx, result.PositionID)
		}
	}
	return result.Status, "", result.PositionID
}
