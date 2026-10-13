// service_execute_meanrevert.go —— D-378a S6：MeanRevert 执行双函数自 service_execute_internal.go
// 整组移出（同包纯搬移零行为变更；check-lines 300 行门禁修法，D-366 先例）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"

	"nox/internal/platform/combo"
	"nox/internal/platform/usdnotional"
	"nox/internal/trading/exec"
)

// ExecuteMeanRevert D-070 均值回归直接执行（绕过 detector，autotrade engine 专用）。
// [exec-entry:gated-body] D-262 M4.3①：调用方=engine_meanrevert/engine_swaparb_armed
// 的 intent.Execute 回调（已过 Gate.Submit）——禁新增非闸调用方。
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
	// D-159 W6a S5：手动 RPC 不绕 CLOSE_ONLY——非 RUNNING 拒绝开仓（fail-closed）。
	state, err := s.checkAutotradeState(ctx)
	if err != nil {
		return "FAILED", fmt.Sprintf("自动交易状态查询失败，按拒绝处理\n原始明细：%v", err), 0
	}
	if state != "RUNNING" {
		return "FAILED", fmt.Sprintf("自动交易未在运行（状态=%s），拒绝开仓", state), 0
	}
	if maxNotional <= 0 {
		maxNotional = 10000.0
	}
	// D-481 普查：本入口无腿原生 symbol——canonical 须即采集键：MT 通道 symbol map
	// 发射键天然命中；若传 CEX venue，调用方须给采集键形 canonical（如 XAUUSDT），
	// MT 形 canonical（XAUUSD）会 miss→quote unavailable（fail-closed 不猜价）。
	qbuy, okBuy := s.qbus.Latest(canonical, venueBuy)
	qsell, okSell := s.qbus.Latest(canonical, venueSell)
	if !okBuy || !okSell {
		return "FAILED", "买/卖腿行情不可得（行情停更或未采集），放弃执行", 0
	}
	csBuy, err := s.legContractSizeOf(venueBuy, qbuy.VenueSymbol)
	if err != nil {
		return "FAILED", fmt.Sprintf("买腿合约大小获取失败\n原始明细：%v", err), 0
	}
	csSell, err := s.legContractSizeOf(venueSell, qsell.VenueSymbol)
	if err != nil {
		return "FAILED", fmt.Sprintf("卖腿合约大小获取失败\n原始明细：%v", err), 0
	}
	// D-329 S2：腿名义两段式匹配与 resolveLegs 同源（matchLegsNotional：pass-1 原样 +
	// pass-2 粗腿锚定）——仅 sellQty 计算与失衡检查收敛；下方 maxNotional clamp 是独立
	// 逻辑原样保留（匹配后在 cap 检查中压回 1 step）。
	qty, sellQty, err := s.matchLegsNotional(venueBuy, qbuy, csBuy, venueSell, qsell, csSell, qty)
	if err != nil {
		return "FAILED", err.Error(), 0
	}
	// CFD notional cap（D-190：两处 cap 检查改 usdnotional.PerLot，USD 统一换算）
	if isMTVenue(venueBuy) {
		perLot, ok := usdnotional.PerLot(canonical, qbuy.Ask, csBuy, s.qbus)
		if !ok {
			return "FAILED", "买腿单手美元名义额不可得，按拒绝处理", 0
		}
		notional := qty * perLot
		if notional > maxNotional {
			qty = s.floorToStepQtyMin(venueBuy, qbuy.VenueSymbol)
		}
	}
	if isMTVenue(venueSell) {
		perLot, ok := usdnotional.PerLot(canonical, qsell.Bid, csSell, s.qbus)
		if !ok {
			return "FAILED", "卖腿单手美元名义额不可得，按拒绝处理", 0
		}
		notional := sellQty * perLot
		if notional > maxNotional {
			sellQty = s.floorToStepQtyMin(venueSell, qsell.VenueSymbol)
		}
	}
	// D-329 R1 返修：clamp 后失衡复检原样恢复——matchLegsNotional 的平衡在 clamp 压腿后
	// 不再成立（非对称形态：MT 买腿压回 1 step + CEX 卖腿不 clamp），复检拦回（R3 锚）。
	ref := qbuy.Ask
	notionalBuy := qty * csBuy * ref
	notionalSell := sellQty * csSell * ref
	maxImb := s.maxLegImbalance
	if maxImb <= 0 {
		maxImb = 2.0
	}
	imb := math.Abs(notionalSell-notionalBuy) / notionalBuy
	if notionalBuy <= 0 || imb > maxImb/100 {
		return "FAILED", fmt.Sprintf("腿名义额失衡：买 $%.2f vs 卖 $%.2f（偏差 %.2f%% 超阈值 %.1f%%）",
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
	if strategyType == "SWAPARB" && len(s.swapArbAllowSpecs()) > 0 {
		if exec.MatchComboAllow(s.swapArbAllowSpecs(), canonical, venueBuy, venueSell) == nil {
			return "FAILED", fmt.Sprintf("组合不在自动交易白名单：%s %s×%s", canonical, venueBuy, venueSell), 0
		}
	}
	// D-311 S6：XASSET_CARRY 组合门禁（手动入口同源）——零信任：空 allowlist 全拒。
	// 本路径无 opportunities 行可查 canonical_sell → 双腿同 canonical 兜底（与 pipeline 门禁同源）。
	if strategyType == "XASSET_CARRY" {
		if exec.MatchXAssetComboAllow(s.xassetAllowSpecs(), canonical, canonical, venueBuy, venueSell) == nil {
			return "FAILED", fmt.Sprintf("组合不在跨品种白名单：%s %s×%s", canonical, venueBuy, venueSell), 0
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
	if result.PositionID > 0 {
		_ = s.store.UpdatePositionMeta(ctx, result.PositionID, strategyType, metaForType(strategyType, openBps))
		// D-327 S1：XASSET_CARRY 补写基准（同 opp 写点——meta 覆写后补，IS NULL 幂等）。
		// D-378a S1：quote 口径（手动路径无 opp 行 → sellCanonical 走 symbol_map 反查，fail-closed）。
		if strategyType == "XASSET_CARRY" {
			if bps, ok := s.quoteEntrySpreadBps(ctx, result.PositionID, canonical, nil); ok {
				_ = s.store.UpdateExpectedEntrySpreadBps(ctx, result.PositionID, bps)
			}
			// D-341 S1：开仓学习样本锚（本路径无 opp 行，canonical 双腿同键兜底）。
			s.ensureEntryGateSnapshotCanonical(ctx, result.PositionID, canonical)
		}
	}
	return result.Status, "", result.PositionID
}
