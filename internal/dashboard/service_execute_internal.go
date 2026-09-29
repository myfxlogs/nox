package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// ExecuteOpportunityInternal 执行机会的核心逻辑（D-070 抽取，供 RPC 和 autotrade engine 复用）。
// 返回 status（EXECUTED/FAILED）、message、positionID（失败时为0）。
func (s *Service) ExecuteOpportunityInternal(ctx context.Context, oppID, requestID string, qty float64) (status, message string, positionID int64) {
	if s.pipeline == nil {
		return "FAILED", "执行器未接线（请联系维护方）", 0
	}
	if qty <= 0 {
		return "FAILED", "下单数量无效", 0
	}
	// D-195-A：统一执行模式检查（手动入口同源，首版只用 global scope）
	if s.gateChecker != nil {
		mode, allowed, cpReason, cpErr := s.gateChecker.CheckGate(ctx)
		if cpErr != nil || !allowed {
			return "FAILED", fmt.Sprintf("当前运行模式禁止开仓（%s，模式=%s）", cpReason, mode), 0
		}
	}
	opps, err := s.store.ListOpportunities(ctx, "", 200, 0)
	if err != nil {
		return "FAILED", fmt.Sprintf("机会列表查询失败\n原始明细：%v", err), 0
	}
	var found *pgstore.Opportunity
	for i := range opps {
		if opps[i].ID == oppID {
			found = &opps[i]
			break
		}
	}
	if found == nil {
		return "FAILED", "机会已失效或不存在（可能刚被执行或超时），请刷新后重试", 0
	}
	// D-159 W6a S5：手动 RPC 不绕 CLOSE_ONLY——非 RUNNING 拒绝开仓（fail-closed）。
	state, err := s.checkAutotradeState(ctx)
	if err != nil {
		return "FAILED", fmt.Sprintf("自动交易状态查询失败，按拒绝处理\n原始明细：%v", err), 0
	}
	if state != "RUNNING" {
		return "FAILED", fmt.Sprintf("自动交易未在运行（状态=%s），拒绝开仓", state), 0
	}
	// D-311 S4/R3：跨 canonical 卖腿——CanonicalSell nil/空回退 Canonical
	//（Revalidate/freezeGuard/combo 门禁同源，P3 单一真相）。
	sellCanonical := found.Canonical
	if found.CanonicalSell != nil && *found.CanonicalSell != "" {
		sellCanonical = *found.CanonicalSell
	}
	if !s.pipeline.Revalidate(found.Canonical, sellCanonical, found.VenueBuy, found.VenueSell, found.Type, found.GrossBps) {
		return "FAILED", "行情已停更或净收益低于门槛，放弃本次执行", 0
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
	if found.Type == "SWAPARB" && len(s.swapArbAllowSpecs()) > 0 {
		if exec.MatchComboAllow(s.swapArbAllowSpecs(), found.Canonical, found.VenueBuy, found.VenueSell) == nil {
			return "FAILED", fmt.Sprintf("组合不在自动交易白名单：%s %s×%s", found.Canonical, found.VenueBuy, found.VenueSell), 0
		}
	}
	// D-311 S6：XASSET_CARRY 组合门禁（手动入口同源）——零信任：空 allowlist 全拒，
	// 命中才放行（区别于 SWAPARB 的 nil=未接线；pipeline 层另有名义额/gross 校验）。
	if found.Type == "XASSET_CARRY" {
		if exec.MatchXAssetComboAllow(s.xassetAllowSpecs(), found.Canonical, sellCanonical, found.VenueBuy, found.VenueSell) == nil {
			return "FAILED", fmt.Sprintf("组合不在跨品种白名单：%s/%s %s×%s", found.Canonical, sellCanonical, found.VenueBuy, found.VenueSell), 0
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
			return "FAILED", "资金费率状态未持续满足条件\n原始明细：" + reason, 0
		}
	}
	result, err := s.pipeline.Execute(ctx, oppID, requestID, found.Type, legs)
	if err != nil {
		switch {
		case errors.Is(err, exec.ErrMaxOpenPositions):
			return "FAILED", "持仓数已达上限，拒绝开仓", 0
		case errors.Is(err, exec.ErrDuplicateExecution):
			return "FAILED", "该机会已有持仓，拒绝重复执行", 0
		case errors.Is(err, exec.ErrEmptyPositionType), errors.Is(err, exec.ErrEmptyLegs):
			return "FAILED", "执行参数缺失（持仓类型或腿为空）", 0
		}
		return "FAILED", fmt.Sprintf("执行失败\n原始明细：%v", err), 0
	}
	// D-070：回填 position 元数据（expected_net_bps/expected_annual_pct；type 已在创建时写入，修复⑭ S4）
	// D-087：typed 化（metaForType 按型构造 PositionMeta）
	if result.PositionID > 0 {
		_ = s.store.UpdatePositionMeta(ctx, result.PositionID, found.Type, metaForType(found.Type, found.NetBps))
		// D-327 S1：XASSET_CARRY 补写基准——metaForType 不产 EntrySpreadBps 且
		// UpdatePositionMeta 无条件覆写该列，必须在 meta 之后补；IS NULL 幂等（已有不覆写）。
		// D-378a S1：锚改纯 quote 口径（fill 口径吸收 venue 成交噪声=493 毒锚根因；
		// 缺 quote→NULL fail-closed）。SWAPARB/CARRY 分支保持 fill 口径 EnsureEntrySpreadFromLegs 不变。
		if found.Type == "XASSET_CARRY" {
			if bps, ok := s.quoteEntrySpreadBps(ctx, result.PositionID, found.Canonical, found); ok {
				_ = s.store.UpdateExpectedEntrySpreadBps(ctx, result.PositionID, bps)
			}
			// D-341 S1：开仓学习样本锚（monitor harvest_floor/noise 落 positions 两列）。
			s.ensureEntryGateSnapshot(ctx, result.PositionID, found)
		}
	}
	return result.Status, "", result.PositionID
}
